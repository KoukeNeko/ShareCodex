// Package desktop finds which account Claude Desktop ran each Claude Code
// session under. Desktop signs in separately from the claude CLI, so its
// sessions cannot be attributed through `claude auth status`. It keeps each
// session's metadata in a folder named after the account and organization;
// the organization UUID is the value the CLI reports as orgId, so both map to
// the same pooled account. Only this session metadata is read, never
// Desktop's settings or credentials.
package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session is one Claude Code session run from Claude Desktop.
type Session struct {
	OrgID        string
	LastActivity time.Time
}

// Dir is Claude Desktop's data folder.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Claude"), nil
}

// FromDesktop reports whether a transcript entrypoint is Claude Desktop's,
// including Cowork and Desktop set up for third-party inference, whose usage
// never draws on a Claude subscription.
func FromDesktop(entrypoint string) bool {
	return strings.HasPrefix(entrypoint, "claude-desktop") || entrypoint == "local-agent"
}

type metadata struct {
	CLISessionID   string `json:"cliSessionId"`
	CreatedAt      int64  `json:"createdAt"`
	LastActivityAt int64  `json:"lastActivityAt"`
}

// Sessions maps Claude Code session IDs to the organization Desktop ran them
// under: Code sessions from claude-code-sessions/<account>/<org>/local_*.json,
// and Cowork sessions from the transcripts Cowork keeps under
// local-agent-mode-sessions/<account>/<org>. A missing folder means Desktop
// is not installed and yields no sessions.
func Sessions(dir string) (map[string]Session, error) {
	out := map[string]Session{}

	code, err := filepath.Glob(filepath.Join(dir, "claude-code-sessions", "*", "*", "local_*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range code {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var m metadata
		// Desktop's files are not a public format; skip what does not parse
		// rather than stop attributing every other session.
		if json.Unmarshal(b, &m) != nil || m.CLISessionID == "" {
			continue
		}
		out[m.CLISessionID] = Session{
			OrgID:        filepath.Base(filepath.Dir(p)),
			LastActivity: epoch(max(m.LastActivityAt, m.CreatedAt)),
		}
	}

	for _, root := range CoworkRoots(dir) {
		org := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(root))))
		transcripts, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
		if err != nil {
			return nil, err
		}
		for _, p := range transcripts {
			info, err := os.Stat(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("stat %s: %w", p, err)
			}
			out[strings.TrimSuffix(filepath.Base(p), ".jsonl")] = Session{OrgID: org, LastActivity: info.ModTime()}
		}
	}
	return out, nil
}

// CoworkRoots returns the Claude Code projects folders of Cowork sessions,
// local-agent-mode-sessions/<account>/<org>/<session>/.claude/projects.
func CoworkRoots(dir string) []string {
	roots, _ := filepath.Glob(filepath.Join(dir, "local-agent-mode-sessions", "*", "*", "local_*", ".claude", "projects"))
	return roots
}

// Latest returns the most recently active session, which tells the account
// Desktop is using now.
func Latest(sessions map[string]Session) (Session, bool) {
	var latest Session
	for _, s := range sessions {
		if s.LastActivity.After(latest.LastActivity) {
			latest = s
		}
	}
	return latest, !latest.LastActivity.IsZero()
}

// epoch converts Desktop's JavaScript timestamps, in milliseconds.
func epoch(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
