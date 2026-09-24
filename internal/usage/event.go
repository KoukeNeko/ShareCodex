// Package usage is the append-only ledger of coding-agent requests.
// Aggregates are always recomputed from events, never stored as the source
// of truth.
package usage

import (
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
)

type Product string

const (
	ProductClaudeCode Product = "claude-code"
	ProductCodex      Product = "codex"
)

// Tokens uses one convention across providers: Input excludes cached input,
// and Output includes ReasoningOutput (reported separately for display).
type Tokens struct {
	Input           int64
	CachedInput     int64
	CacheWrite      int64
	Output          int64
	ReasoningOutput int64
}

func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		Input:           t.Input + o.Input,
		CachedInput:     t.CachedInput + o.CachedInput,
		CacheWrite:      t.CacheWrite + o.CacheWrite,
		Output:          t.Output + o.Output,
		ReasoningOutput: t.ReasoningOutput + o.ReasoningOutput,
	}
}

// Event is one model request observed on a device. Prompts, working
// directories and project paths are never recorded.
type Event struct {
	// DedupeKey is unique per upstream request and is built by the provider
	// adapter that parsed it, so re-reading a file is idempotent.
	DedupeKey string

	// AccountRefHash is empty until the account timeline resolves it.
	AccountRefHash string
	DeviceID       string

	Provider   account.Provider
	Product    Product
	Originator string
	SessionID  string
	Model      string
	OccurredAt time.Time

	Tokens Tokens
}
