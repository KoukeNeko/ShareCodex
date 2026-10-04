// Package transcript parses Claude Code session transcripts (JSONL under
// $CLAUDE_CONFIG_DIR/projects) into usage events.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// LimitSource labels limit events read from Claude Code transcripts.
const LimitSource = "claude-transcript"

type Result struct {
	Events []usage.Event
	// LimitEvents are rate-limit, overload, and authentication failures that
	// Claude Code logged as synthetic error messages.
	LimitEvents []usage.LimitEvent
	// BadLines counts complete lines that were not valid JSON.
	BadLines int
}

type line struct {
	Type       string    `json:"type"`
	SessionID  string    `json:"sessionId"`
	RequestID  string    `json:"requestId"`
	Timestamp  time.Time `json:"timestamp"`
	Entrypoint string    `json:"entrypoint"`
	UUID       string    `json:"uuid"`
	// PerTurnEffort is the effort level of the turn, null when none was set.
	PerTurnEffort  *string `json:"perTurnEffort"`
	IsAPIError     bool    `json:"isApiErrorMessage"`
	Error          string  `json:"error"`
	APIErrorStatus int     `json:"apiErrorStatus"`
	QuotaLimits    *struct {
		RateLimitType string `json:"rateLimitType"`
	} `json:"quotaLimits"`
	Message *struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// anthropicModel matches Anthropic's own models, whatever the family (opus,
// sonnet, haiku, fable, …): claude-<family>-<version>. OpenCodex's
// claude-ocx-<service>--<model> names are not Anthropic's.
var anthropicModel = regexp.MustCompile(`^claude-[a-z]+-\d`)

// Claude Code logs neither the API address nor how it was launched, but
// gateways answer with their own message IDs and no request ID: OpenCodex
// with 32 hex digits, Ollama (`ollama launch claude`) with 24. OpenCodex also
// names the models it routes `ocx-claude-<service>--<model>` (older builds
// `claude-ocx-…`). Anthropic's own IDs look like msg_011C… with a req_…
// request ID.
var (
	openCodexModel = regexp.MustCompile(`^(ocx-claude|claude-ocx)-.+--.+`)
	// OpenCodex's "native" service is the ChatGPT subscription Codex is
	// signed into, so those requests draw on a ChatGPT account's quota.
	openCodexNative    = regexp.MustCompile(`^(?:ocx-claude|claude-ocx)-native--(.+)$`)
	openCodexMessageID = regexp.MustCompile(`^msg_[0-9a-f]{32}$`)
	ollamaMessageID    = regexp.MustCompile(`^msg_[0-9a-f]{24}$`)
)

// gateway names the service a request went through, or "" when its log
// line does not show one.
func gateway(model, messageID, requestID string) string {
	switch {
	case openCodexModel.MatchString(model):
		return usage.GatewayOpenCodex
	case requestID != "":
		return ""
	case openCodexMessageID.MatchString(messageID):
		return usage.GatewayOpenCodex
	case ollamaMessageID.MatchString(messageID):
		return usage.GatewayOllama
	}
	return ""
}

// assistantMarker lets Parse skip decoding user and tool lines, which make up
// most of a transcript's bytes.
var assistantMarker = []byte(`"assistant"`)

// Parse reads a whole transcript. Claude Code writes one line per content
// block of a response; while streaming, earlier lines carry a partial
// output count, so each response keeps the line with the most output
// tokens. A trailing line without a newline is ignored because it may still
// be being written.
func Parse(r io.Reader) (Result, error) {
	var res Result
	index := make(map[string]int)

	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("read transcript: %w", err)
		}
		if !bytes.Contains(raw, assistantMarker) {
			continue
		}

		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			res.BadLines++
			continue
		}
		if l.Type != "assistant" || l.Message == nil {
			continue
		}
		if l.IsAPIError {
			if le, ok := limitEvent(l); ok {
				res.LimitEvents = append(res.LimitEvents, le)
			}
			continue
		}
		if l.Message.Usage == nil {
			continue
		}
		// "<synthetic>" messages are made up by Claude Code and never reach
		// any API.
		if l.Message.ID == "" || l.Message.Model == "" || l.Message.Model == "<synthetic>" {
			continue
		}

		key := "claude:" + l.Message.ID + ":" + l.RequestID
		u := l.Message.Usage
		if i, ok := index[key]; ok {
			if u.OutputTokens > res.Events[i].Tokens.Output {
				res.Events[i].Tokens.Output = u.OutputTokens
			}
			continue
		}
		index[key] = len(res.Events)
		e := usage.Event{
			DedupeKey:  key,
			Provider:   account.ProviderAnthropic,
			Product:    usage.ProductClaudeCode,
			Originator: l.Entrypoint,
			SessionID:  l.SessionID,
			RequestID:  l.RequestID,
			Model:      l.Message.Model,
			Effort:     effort(l.PerTurnEffort),
			OccurredAt: l.Timestamp,
			// Claude Code can be pointed at other vendors' models through a
			// gateway; only Anthropic models draw on a Claude subscription.
			ThirdParty: !anthropicModel.MatchString(l.Message.Model) || openCodexModel.MatchString(l.Message.Model),
			Gateway:    gateway(l.Message.Model, l.Message.ID, l.RequestID),
			Tokens: usage.Tokens{
				Input:       u.InputTokens,
				CachedInput: u.CacheReadInputTokens,
				CacheWrite:  u.CacheCreationInputTokens,
				Output:      u.OutputTokens,
			},
		}
		if m := openCodexNative.FindStringSubmatch(l.Message.Model); m != nil {
			// Counted on the ChatGPT account under the model's own name,
			// like the same model used from Codex.
			e.Provider, e.Model, e.ThirdParty = account.ProviderOpenAI, m[1], false
		}
		res.Events = append(res.Events, e)
	}
	return res, nil
}

func effort(level *string) string {
	if level == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*level))
}

// limitEvent turns an error message into a limit event, or reports false for
// errors that say nothing about limits (network failures, bad requests). Only
// codes are kept; the message text is never stored.
func limitEvent(l line) (usage.LimitEvent, bool) {
	var blocks []struct {
		Text string `json:"text"`
	}
	// The text only refines the classification; without it the codes decide.
	_ = json.Unmarshal(l.Message.Content, &blocks)
	var text string
	if len(blocks) > 0 {
		text = blocks[0].Text
	}
	var rateLimitType string
	if l.QuotaLimits != nil {
		rateLimitType = l.QuotaLimits.RateLimitType
	}
	kind, evidence, ok := classifyLimit(l.Error, l.APIErrorStatus, rateLimitType, text)
	if !ok || l.UUID == "" {
		return usage.LimitEvent{}, false
	}
	return usage.LimitEvent{
		DedupeKey:  "claude-limit:" + l.UUID,
		Provider:   account.ProviderAnthropic,
		Originator: l.Entrypoint,
		OccurredAt: l.Timestamp,
		SessionID:  l.SessionID,
		RequestID:  l.RequestID,
		Kind:       kind,
		Source:     LimitSource,
		Evidence:   evidence,
		HTTPStatus: l.APIErrorStatus,
	}, true
}

// classifyLimit names the limit behind an error message. The evidence is a
// code for how it was told: Claude's rate limit type, a fixed marker for
// messages logged without one, or "gateway" for throttling by a proxy, which
// is never a subscription limit.
func classifyLimit(errCode string, status int, rateLimitType, text string) (usage.LimitKind, string, bool) {
	text = strings.ReplaceAll(text, "’", "'")
	if errCode == "rate_limit" {
		switch {
		case rateLimitType == "five_hour":
			return usage.LimitFiveHour, rateLimitType, true
		case rateLimitType == "seven_day":
			return usage.LimitWeekly, rateLimitType, true
		case strings.HasPrefix(rateLimitType, "seven_day_") || strings.HasPrefix(rateLimitType, "five_hour_"):
			return usage.LimitModelSpecific, rateLimitType, true
		case rateLimitType != "":
			return usage.LimitUnknown, rateLimitType, true
		case strings.HasPrefix(text, "You've hit your session limit"):
			return usage.LimitFiveHour, "session_limit", true
		case strings.HasPrefix(text, "You've hit your weekly limit"):
			return usage.LimitWeekly, "weekly_limit", true
		case strings.Contains(text, "monthly spend limit") || strings.Contains(text, "out of extra usage"):
			return usage.LimitUnknown, "extra_usage", true
		case strings.Contains(text, "not your usage limit") || strings.Contains(text, "Request rejected (429)"):
			return usage.LimitProvider429, "gateway", true
		}
	}
	switch {
	case status == 529 || strings.Contains(text, "Overloaded"):
		return usage.LimitOverload, "overloaded", true
	case errCode == "authentication_failed" || status == 401 || status == 403:
		return usage.LimitAuth, "auth", true
	}
	return "", "", false
}

// Roots returns the directories holding transcripts. CLAUDE_CONFIG_DIR may
// list several directories separated by commas; without it Claude Code uses
// ~/.claude, and older versions ~/.config/claude.
func Roots() []string {
	var dirs []string
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		for _, d := range strings.Split(env, ",") {
			if d = strings.TrimSpace(d); d != "" {
				dirs = append(dirs, d)
			}
		}
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = []string{filepath.Join(home, ".claude"), filepath.Join(home, ".config", "claude")}
	}
	roots := make([]string, len(dirs))
	for i, d := range dirs {
		roots[i] = filepath.Join(d, "projects")
	}
	return roots
}
