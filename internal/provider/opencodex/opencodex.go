// Package opencodex reads OpenCodex's own request log, to tell which account
// served a request a Claude client sent through it. OpenCodex can spread one
// model over several accounts, and the client's log does not say which one
// answered. Only request times, token counts and OpenCodex's account labels
// are read; never its credentials.
package opencodex

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// MainChatGPT is the label OpenCodex gives requests served by the ChatGPT
// account Codex itself is signed into; its other ChatGPT accounts are
// labelled "openai-<id>".
const MainChatGPT = "openai"

// Record is one request OpenCodex answered.
type Record struct {
	At time.Time
	// Provider is OpenCodex's label for the account or service that served
	// the request.
	Provider string
	// Input counts the prompt's tokens, cached and cache writes included.
	Input, CacheRead, CacheWrite, Output int64
}

// Path is OpenCodex's request log.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".opencodex", "usage.jsonl"), nil
}

type line struct {
	Timestamp int64  `json:"timestamp"`
	Provider  string `json:"provider"`
	Status    int    `json:"status"`
	Usage     *struct {
		InputTokens              int64 `json:"inputTokens"`
		OutputTokens             int64 `json:"outputTokens"`
		CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
		CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
	} `json:"usage"`
}

// Read returns the successful requests with reported usage. A missing log
// means OpenCodex is not in use: no records and no error.
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parse(f)
}

func parse(r io.Reader) ([]Record, error) {
	var out []Record
	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		var l line
		if json.Unmarshal(raw, &l) != nil || l.Status != 200 || l.Usage == nil || l.Provider == "" {
			continue
		}
		out = append(out, Record{
			At:         time.UnixMilli(l.Timestamp),
			Provider:   l.Provider,
			Input:      l.Usage.InputTokens,
			CacheRead:  l.Usage.CacheReadInputTokens,
			CacheWrite: l.Usage.CacheCreationInputTokens,
			Output:     l.Usage.OutputTokens,
		})
	}
	return out, nil
}

// matchWindow allows for the client logging a response a while after
// OpenCodex recorded its request.
const matchWindow = 10 * time.Minute

// Match finds the record of a request a client logged, by its exact token
// counts near its time. It reports false unless exactly one record fits.
func Match(records []Record, at time.Time, t usage.Tokens) (Record, bool) {
	var found []Record
	for _, r := range records {
		d := r.At.Sub(at)
		if d < -matchWindow || d > matchWindow {
			continue
		}
		if r.CacheRead != t.CachedInput || r.CacheWrite != t.CacheWrite || r.Output != t.Output {
			continue
		}
		// OpenCodex counts the whole prompt; clients log the uncached part.
		if r.Input != t.Input && r.Input-r.CacheRead-r.CacheWrite != t.Input {
			continue
		}
		found = append(found, r)
	}
	if len(found) != 1 {
		return Record{}, false
	}
	return found[0], true
}
