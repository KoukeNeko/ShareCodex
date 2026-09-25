// Package syncapi is the wire contract between the desktop client and the
// server. It uses its own DTOs so that changing a domain type never changes
// the network protocol by accident.
package syncapi

import (
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// Version is bumped on any incompatible change; the server rejects other
// versions with 426 Upgrade Required.
const Version = 1

const (
	PathPair     = "/internal/api/v1/pair"
	PathSync     = "/internal/api/v1/sync"
	PathOverview = "/internal/api/v1/overview"
	PathInvite   = "/internal/api/v1/invite"
	PathJoin     = "/join/"
)

type Event struct {
	DedupeKey       string    `json:"dedupe_key"`
	AccountRefHash  string    `json:"account_ref_hash"`
	Provider        string    `json:"provider"`
	Product         string    `json:"product"`
	Originator      string    `json:"originator"`
	SessionID       string    `json:"session_id"`
	Model           string    `json:"model"`
	OccurredAt      time.Time `json:"occurred_at"`
	Input           int64     `json:"input"`
	CachedInput     int64     `json:"cached_input"`
	CacheWrite      int64     `json:"cache_write"`
	Output          int64     `json:"output"`
	ReasoningOutput int64     `json:"reasoning_output"`
}

type Bucket struct {
	Key           string     `json:"key"`
	UsedPercent   *float64   `json:"used_percent,omitempty"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	WindowMinutes *int       `json:"window_minutes,omitempty"`
}

type Snapshot struct {
	AccountRefHash string    `json:"account_ref_hash"`
	Provider       string    `json:"provider"`
	Source         string    `json:"source"`
	ObservedAt     time.Time `json:"observed_at"`
	Buckets        []Bucket  `json:"buckets"`
}

type Observation struct {
	Provider string `json:"provider"`
	// Source is the app that reported the account; empty for the CLI.
	Source         string    `json:"source,omitempty"`
	AccountRefHash string    `json:"account_ref_hash"`
	Hint           string    `json:"hint"`
	PlanType       string    `json:"plan_type"`
	ObservedAt     time.Time `json:"observed_at"`
}

type SyncRequest struct {
	Version      int           `json:"version"`
	Observations []Observation `json:"observations"`
	Events       []Event       `json:"events"`
	Snapshots    []Snapshot    `json:"snapshots"`
}

type SyncResponse struct {
	Accepted int `json:"accepted"`
}

type PairRequest struct {
	Version    int    `json:"version"`
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
}

type PairResponse struct {
	DeviceID   string `json:"device_id"`
	PersonID   string `json:"person_id"`
	PersonName string `json:"person_name"`
	Token      string `json:"token"`
}

// InviteResponse is a join link code for another device of the same
// person. The client adds its own server URL to make the link.
type InviteResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Error struct {
	Error string `json:"error"`
}

// Overview is everything the popup shows, computed by the server so the
// frontend only renders.
type Overview struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Accounts    []AccountOverview `json:"accounts"`
}

type AccountOverview struct {
	ID       string           `json:"id"`
	Provider string           `json:"provider"`
	Label    string           `json:"label"`
	PlanType string           `json:"plan_type"`
	Buckets  []BucketOverview `json:"buckets"`
	// ActiveUsers are the people whose CLI is signed into this account right
	// now, the viewer first.
	ActiveUsers []ActiveUser `json:"active_users"`
}

// ActiveUser is one person signed into an account, with the devices they
// use it on.
type ActiveUser struct {
	PersonID string   `json:"person_id"`
	Name     string   `json:"name"`
	IsYou    bool     `json:"is_you"`
	Devices  []string `json:"devices"`
}

type BucketOverview struct {
	Key           string     `json:"key"`
	UsedPercent   float64    `json:"used_percent"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	WindowMinutes int        `json:"window_minutes"`
	ObservedAt    time.Time  `json:"observed_at"`
	// Reset is true when the window closed after the last observation, so
	// UsedPercent is 0 until someone reports again.
	Reset bool `json:"reset"`
	// UnattributedPercent is quota consumed in the window with no matching
	// events from any member's device.
	UnattributedPercent float64       `json:"unattributed_percent"`
	Members             []MemberShare `json:"members"`
	Models              []ModelUsage  `json:"models"`
}

// ModelUsage is one model's usage within a quota window, across members.
type ModelUsage struct {
	Model    string `json:"model"`
	Requests int    `json:"requests"`
	// Tokens counts input, cached input, cache writes and output together.
	Tokens int64 `json:"tokens"`
	// UsedPercent is an estimate, apportioned by weighted token cost like
	// MemberShare.UsedPercent.
	UsedPercent float64 `json:"used_percent"`
}

type MemberShare struct {
	PersonID    string  `json:"person_id"`
	Name        string  `json:"name"`
	IsYou       bool    `json:"is_you"`
	ShareWeight float64 `json:"share_weight"`
	// AllottedPercent is the member's share of the whole bucket.
	AllottedPercent float64 `json:"allotted_percent"`
	// UsedPercent is an estimate, apportioned by weighted token cost.
	UsedPercent float64 `json:"used_percent"`
	Requests    int     `json:"requests"`
	// Models splits UsedPercent by model, in the bucket's Models order.
	Models []MemberModel `json:"models"`
}

type MemberModel struct {
	Model       string  `json:"model"`
	UsedPercent float64 `json:"used_percent"`
}

func FromEvent(e usage.Event) Event {
	return Event{
		DedupeKey:       e.DedupeKey,
		AccountRefHash:  e.AccountRefHash,
		Provider:        string(e.Provider),
		Product:         string(e.Product),
		Originator:      e.Originator,
		SessionID:       e.SessionID,
		Model:           e.Model,
		OccurredAt:      e.OccurredAt.UTC(),
		Input:           e.Tokens.Input,
		CachedInput:     e.Tokens.CachedInput,
		CacheWrite:      e.Tokens.CacheWrite,
		Output:          e.Tokens.Output,
		ReasoningOutput: e.Tokens.ReasoningOutput,
	}
}

func (e Event) ToDomain() usage.Event {
	return usage.Event{
		DedupeKey:      e.DedupeKey,
		AccountRefHash: e.AccountRefHash,
		Provider:       account.Provider(e.Provider),
		Product:        usage.Product(e.Product),
		Originator:     e.Originator,
		SessionID:      e.SessionID,
		Model:          e.Model,
		OccurredAt:     e.OccurredAt,
		Tokens: usage.Tokens{
			Input:           e.Input,
			CachedInput:     e.CachedInput,
			CacheWrite:      e.CacheWrite,
			Output:          e.Output,
			ReasoningOutput: e.ReasoningOutput,
		},
	}
}

func FromSnapshot(s quota.Snapshot) Snapshot {
	out := Snapshot{
		AccountRefHash: s.AccountRefHash,
		Provider:       string(s.Provider),
		Source:         string(s.Source),
		ObservedAt:     s.ObservedAt.UTC(),
	}
	for _, b := range s.Buckets {
		out.Buckets = append(out.Buckets, Bucket{
			Key:           string(b.Key),
			UsedPercent:   b.UsedPercent,
			ResetsAt:      b.ResetsAt,
			WindowMinutes: b.WindowMinutes,
		})
	}
	return out
}

func (s Snapshot) ToDomain() quota.Snapshot {
	out := quota.Snapshot{
		AccountRefHash: s.AccountRefHash,
		Provider:       account.Provider(s.Provider),
		Source:         quota.Source(s.Source),
		ObservedAt:     s.ObservedAt,
	}
	for _, b := range s.Buckets {
		out.Buckets = append(out.Buckets, quota.Bucket{
			Key:           quota.BucketKey(b.Key),
			UsedPercent:   b.UsedPercent,
			ResetsAt:      b.ResetsAt,
			WindowMinutes: b.WindowMinutes,
		})
	}
	return out
}

func FromObservation(o account.Observation) Observation {
	return Observation{
		Provider:       string(o.Provider),
		Source:         string(o.Source),
		AccountRefHash: o.ExternalRefHash,
		Hint:           o.Hint,
		PlanType:       o.PlanType,
		ObservedAt:     o.ObservedAt.UTC(),
	}
}
