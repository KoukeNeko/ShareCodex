// Package statusline captures the rate limits Claude Code passes to its
// statusLine command. Claude Code runs the shim on every status update; the
// shim saves the limits to a spool file the agent picks up, then chains to
// the user's own statusLine command so their status bar keeps working.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/atomicfile"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

type window struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

type rateLimits struct {
	FiveHour *window `json:"five_hour,omitempty"`
	SevenDay *window `json:"seven_day,omitempty"`
}

type input struct {
	SessionID  string      `json:"session_id"`
	RateLimits *rateLimits `json:"rate_limits"`
}

type spoolEntry struct {
	SessionID  string      `json:"session_id"`
	ObservedAt time.Time   `json:"observed_at"`
	RateLimits *rateLimits `json:"rate_limits"`
}

// Run is the shim. It must never break Claude Code's status bar, so spool
// errors are reported on stderr only and the chained command still runs.
func Run(stdin io.Reader, stdout, stderr io.Writer, spoolDir, chainCommand string) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "sharecodex statusline:", err)
		return
	}
	if err := spool(raw, spoolDir, time.Now()); err != nil {
		fmt.Fprintln(stderr, "sharecodex statusline:", err)
	}
	if chainCommand != "" {
		chain(raw, stdout, stderr, chainCommand)
	}
}

func spool(raw []byte, dir string, now time.Time) error {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("decode statusLine input: %w", err)
	}
	// Rate limits appear only for Pro/Max accounts, after the first reply.
	if in.RateLimits == nil || in.SessionID == "" || strings.ContainsAny(in.SessionID, `/\`) {
		return nil
	}
	p := filepath.Join(dir, in.SessionID+".json")

	// The shim runs on every status refresh; only unchanged limits are
	// skipped so the agent does not ingest a snapshot per keystroke.
	if prev, err := os.ReadFile(p); err == nil {
		var old spoolEntry
		if json.Unmarshal(prev, &old) == nil && sameLimits(old.RateLimits, in.RateLimits) {
			return nil
		}
	}
	b, err := json.Marshal(spoolEntry{SessionID: in.SessionID, ObservedAt: now.UTC(), RateLimits: in.RateLimits})
	if err != nil {
		return err
	}
	return atomicfile.Write(p, b, 0o600)
}

func sameLimits(a, b *rateLimits) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func chain(raw []byte, stdout, stderr io.Writer, command string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(stderr, "sharecodex statusline: chained command:", err)
	}
}

// ReadSpool returns one snapshot per spooled session. Account resolution is
// left to the caller, which knows the device's account timeline.
func ReadSpool(dir string) ([]quota.Snapshot, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []quota.Snapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var entry spoolEntry
		if err := json.Unmarshal(b, &entry); err != nil || entry.RateLimits == nil {
			continue
		}
		if s, ok := toSnapshot(entry); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// PruneSpool removes spool files for sessions idle longer than maxAge.
func PruneSpool(dir string, maxAge time.Duration, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func toSnapshot(e spoolEntry) (quota.Snapshot, bool) {
	var buckets []quota.Bucket
	add := func(key quota.BucketKey, minutes int, w *window) {
		if w == nil {
			return
		}
		used, mins := w.UsedPercentage, minutes
		b := quota.Bucket{Key: key, UsedPercent: &used, WindowMinutes: &mins}
		if w.ResetsAt > 0 {
			t := time.Unix(w.ResetsAt, 0).UTC()
			b.ResetsAt = &t
		}
		buckets = append(buckets, b)
	}
	add(quota.BucketFiveHour, 300, e.RateLimits.FiveHour)
	add(quota.BucketWeekly, 10080, e.RateLimits.SevenDay)
	if len(buckets) == 0 {
		return quota.Snapshot{}, false
	}
	return quota.Snapshot{
		Provider:   account.ProviderAnthropic,
		ObservedAt: e.ObservedAt,
		Source:     quota.SourceClaudeStatusLine,
		Buckets:    buckets,
	}, true
}
