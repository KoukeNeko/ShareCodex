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

type Result struct {
	Events []usage.Event
	// BadLines counts complete lines that were not valid JSON.
	BadLines int
}

type line struct {
	Type       string    `json:"type"`
	SessionID  string    `json:"sessionId"`
	RequestID  string    `json:"requestId"`
	Timestamp  time.Time `json:"timestamp"`
	Entrypoint string    `json:"entrypoint"`
	Message    *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var anthropicModel = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-`)

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
		if l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil {
			continue
		}
		// Claude Code can be pointed at other vendors' models through a
		// gateway; only Anthropic models draw on a Claude subscription. This
		// also drops "<synthetic>" messages, which never reach the API.
		if !anthropicModel.MatchString(l.Message.Model) || l.Message.ID == "" {
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
		res.Events = append(res.Events, usage.Event{
			DedupeKey:  key,
			Provider:   account.ProviderAnthropic,
			Product:    usage.ProductClaudeCode,
			Originator: l.Entrypoint,
			SessionID:  l.SessionID,
			Model:      l.Message.Model,
			OccurredAt: l.Timestamp,
			Tokens: usage.Tokens{
				Input:       u.InputTokens,
				CachedInput: u.CacheReadInputTokens,
				CacheWrite:  u.CacheCreationInputTokens,
				Output:      u.OutputTokens,
			},
		})
	}
	return res, nil
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
