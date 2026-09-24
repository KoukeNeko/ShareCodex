// Package rollout parses Codex session rollout files (JSONL under
// $CODEX_HOME/sessions and archived_sessions) into usage events and quota
// snapshots.
package rollout

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// Result is everything usable in one rollout file.
type Result struct {
	SessionID string
	// Skipped is set when the session ran against a non-OpenAI model
	// provider, so none of it counts against a ChatGPT subscription.
	Skipped   bool
	Events    []usage.Event
	Snapshots []quota.Snapshot
	// BadLines counts complete lines that were not valid JSON.
	BadLines int
}

type line struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID            string `json:"id"`
	SessionID     string `json:"session_id"`
	Originator    string `json:"originator"`
	ModelProvider string `json:"model_provider"`
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func (u tokenUsage) sub(o tokenUsage) tokenUsage {
	return tokenUsage{
		InputTokens:           u.InputTokens - o.InputTokens,
		CachedInputTokens:     u.CachedInputTokens - o.CachedInputTokens,
		CacheWriteInputTokens: u.CacheWriteInputTokens - o.CacheWriteInputTokens,
		OutputTokens:          u.OutputTokens - o.OutputTokens,
		ReasoningOutputTokens: u.ReasoningOutputTokens - o.ReasoningOutputTokens,
		TotalTokens:           u.TotalTokens - o.TotalTokens,
	}
}

// tokens converts OpenAI's convention (cached input is part of input) to
// usage.Tokens (input excludes cached input).
func (u tokenUsage) tokens() usage.Tokens {
	return usage.Tokens{
		Input:           u.InputTokens - u.CachedInputTokens,
		CachedInput:     u.CachedInputTokens,
		CacheWrite:      u.CacheWriteInputTokens,
		Output:          u.OutputTokens,
		ReasoningOutput: u.ReasoningOutputTokens,
	}
}

type usageRecord struct {
	ResponseID string     `json:"response_id"`
	SessionID  string     `json:"session_id"`
	Usage      tokenUsage `json:"usage"`
}

type eventMsg struct {
	Type string `json:"type"`
	Info *struct {
		Total tokenUsage `json:"total_token_usage"`
		Last  tokenUsage `json:"last_token_usage"`
	} `json:"info"`
	RateLimits *rateLimits `json:"rate_limits"`
}

type rateLimits struct {
	LimitID   string       `json:"limit_id"`
	Primary   *limitWindow `json:"primary"`
	Secondary *limitWindow `json:"secondary"`
}

type limitWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// Parse reads a whole rollout file. A trailing line without a newline is
// ignored because Codex may still be writing it; the next scan picks it up.
func Parse(r io.Reader) (Result, error) {
	var (
		res        Result
		originator string
		model      = "unknown"
		records    []usage.Event
		fallback   []usage.Event
		prevTotal  *tokenUsage
	)

	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("read rollout: %w", err)
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}

		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			res.BadLines++
			continue
		}

		switch l.Type {
		case "session_meta":
			var m sessionMeta
			if err := json.Unmarshal(l.Payload, &m); err != nil {
				res.BadLines++
				continue
			}
			res.SessionID = cmp.Or(m.SessionID, m.ID)
			originator = m.Originator
			// Older Codex builds omit model_provider; they only spoke to OpenAI.
			if m.ModelProvider != "" && m.ModelProvider != "openai" {
				return Result{SessionID: res.SessionID, Skipped: true}, nil
			}

		case "turn_context":
			var tc struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(l.Payload, &tc); err == nil && tc.Model != "" {
				model = tc.Model
			}

		case "token_usage_record":
			var rec usageRecord
			if err := json.Unmarshal(l.Payload, &rec); err != nil || rec.ResponseID == "" {
				res.BadLines++
				continue
			}
			records = append(records, usage.Event{
				DedupeKey:  "codex:" + rec.ResponseID,
				SessionID:  cmp.Or(rec.SessionID, res.SessionID),
				Model:      model,
				OccurredAt: l.Timestamp,
				Tokens:     rec.Usage.tokens(),
			})

		case "event_msg":
			var msg eventMsg
			if err := json.Unmarshal(l.Payload, &msg); err != nil || msg.Type != "token_count" {
				continue
			}
			if s, ok := snapshotFrom(msg.RateLimits, l.Timestamp); ok {
				res.Snapshots = append(res.Snapshots, s)
			}
			if msg.Info == nil {
				continue
			}
			total := msg.Info.Total
			if prevTotal == nil {
				// The first count may include totals inherited from a parent
				// session (sub-agents); only the latest request is new here.
				base := total.sub(msg.Info.Last)
				prevTotal = &base
			}
			if total.TotalTokens <= prevTotal.TotalTokens {
				continue
			}
			fallback = append(fallback, usage.Event{
				DedupeKey:  fmt.Sprintf("codex:%s:tc:%d", res.SessionID, total.TotalTokens),
				SessionID:  res.SessionID,
				Model:      model,
				OccurredAt: l.Timestamp,
				Tokens:     total.sub(*prevTotal).tokens(),
			})
			prevTotal = &total
		}
	}

	// Files written by newer Codex carry per-request records; counting the
	// cumulative token_count as well would double every request.
	res.Events = records
	if len(records) == 0 {
		res.Events = fallback
	}
	for i := range res.Events {
		res.Events[i].Provider = account.ProviderOpenAI
		res.Events[i].Product = usage.ProductCodex
		res.Events[i].Originator = originator
	}
	return res, nil
}

func snapshotFrom(rl *rateLimits, at time.Time) (quota.Snapshot, bool) {
	// Other limit IDs are per-model limits that would overwrite the account's
	// main 5h/weekly windows if merged into the same buckets.
	if rl == nil || (rl.LimitID != "" && rl.LimitID != "codex") {
		return quota.Snapshot{}, false
	}
	var buckets []quota.Bucket
	for _, w := range []*limitWindow{rl.Primary, rl.Secondary} {
		if w != nil {
			buckets = append(buckets, BucketFromWindow(w.UsedPercent, w.WindowMinutes, w.ResetsAt))
		}
	}
	if len(buckets) == 0 {
		return quota.Snapshot{}, false
	}
	return quota.Snapshot{
		Provider:   account.ProviderOpenAI,
		ObservedAt: at,
		Source:     quota.SourceCodexRollout,
		Buckets:    buckets,
	}, true
}

// BucketFromWindow maps a Codex limit window to a bucket. Codex reports
// windows by length rather than by name, so the length decides the key.
func BucketFromWindow(usedPercent float64, windowMinutes int, resetsAtUnix int64) quota.Bucket {
	key := quota.BucketKey(fmt.Sprintf("window_%dm", windowMinutes))
	switch windowMinutes {
	case 300:
		key = quota.BucketFiveHour
	case 10080:
		key = quota.BucketWeekly
	}
	b := quota.Bucket{Key: key, UsedPercent: &usedPercent, WindowMinutes: &windowMinutes}
	if resetsAtUnix > 0 {
		t := time.Unix(resetsAtUnix, 0).UTC()
		b.ResetsAt = &t
	}
	return b
}

// Roots returns the directories holding rollout files, honouring CODEX_HOME.
func Roots() []string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		home = filepath.Join(userHome, ".codex")
	}
	return []string{filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")}
}
