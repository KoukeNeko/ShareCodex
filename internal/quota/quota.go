// Package quota describes how much of an account's subscription allowance
// remains. It is kept separate from usage: usage says what was consumed,
// quota says what the provider reports is left.
package quota

import (
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
)

type BucketKey string

const (
	BucketFiveHour BucketKey = "five_hour"
	BucketWeekly   BucketKey = "weekly"
)

type Source string

const (
	SourceCodexRollout     Source = "codex-rollout"
	SourceCodexRPC         Source = "codex-rpc"
	SourceClaudeStatusLine Source = "claude-statusline"
	// SourceClaudeOAuthUsage is Anthropic's own usage endpoint, read with
	// Claude Code's credential or a ShareCodex sign-in. Unlike the statusLine
	// it needs no session, so it also reports usage that only Claude Desktop
	// produces.
	SourceClaudeOAuthUsage Source = "claude-oauth-usage"
)

// Window lengths of the rate limits Claude reports. Codex reports each
// window's own length, so these serve Claude's sources only.
const (
	FiveHourMinutes = 300
	WeeklyMinutes   = 10080
)

// Bucket is one limit window. Providers may omit any field, so optional
// values are pointers rather than zero values.
type Bucket struct {
	Key           BucketKey
	UsedPercent   *float64
	ResetsAt      *time.Time
	WindowMinutes *int
}

type Snapshot struct {
	AccountRefHash string
	// AccountHint and PlanType name an account the device knows only from a
	// sign-in, not from an observation. They travel to the server with the
	// snapshot and are not kept in the ledger.
	AccountHint string
	PlanType    string
	DeviceID    string
	Provider    account.Provider
	ObservedAt  time.Time
	Source      Source
	Buckets     []Bucket
}

// Observed is a bucket together with when it was reported.
type Observed struct {
	Bucket
	ObservedAt time.Time
}

// Latest merges snapshots reported by any device for one account into each
// bucket's current value.
//
// Readings of one window can disagree: a statusLine reading carries the
// percentage of its session's last response, which can be long out of date,
// while it is observed now. Usage within a window only rises, so among
// readings of the current window the highest percentage is the most recent
// truth. A reading of a later window replaces the earlier window's (a reset).
// Readings belong to one window when their reset times are less than half a
// window apart, which absorbs jitter in the reported reset time. A bucket
// without a reset time or percentage falls back to the newest reading.
func Latest(snapshots []Snapshot) map[BucketKey]Observed {
	latest := make(map[BucketKey]Observed)
	for _, s := range snapshots {
		for _, b := range s.Buckets {
			next := Observed{Bucket: b, ObservedAt: s.ObservedAt}
			cur, ok := latest[b.Key]
			if !ok || replaces(next, cur) {
				latest[b.Key] = next
			}
		}
	}
	return latest
}

// replaces reports whether a reading should replace the current value.
func replaces(next, cur Observed) bool {
	comparable := next.ResetsAt != nil && cur.ResetsAt != nil && next.WindowMinutes != nil &&
		next.UsedPercent != nil && cur.UsedPercent != nil
	if !comparable {
		return next.ObservedAt.After(cur.ObservedAt)
	}
	half := time.Duration(*next.WindowMinutes) * time.Minute / 2
	switch d := next.ResetsAt.Sub(*cur.ResetsAt); {
	case d > half:
		return true // a later window
	case d < -half:
		return false // an earlier window
	}
	if *next.UsedPercent != *cur.UsedPercent {
		return *next.UsedPercent > *cur.UsedPercent
	}
	return next.ObservedAt.After(cur.ObservedAt)
}

// HasReset reports whether the window closed after the observation, in which
// case the reported percentage no longer applies.
func (o Observed) HasReset(now time.Time) bool {
	return o.ResetsAt != nil && !now.Before(*o.ResetsAt)
}

// Window returns the time span the bucket covers: the WindowMinutes before
// ResetsAt, ending no later than now. Without a reset time it is the
// WindowMinutes before now.
func (o Observed) Window(now time.Time) (start, end time.Time) {
	length := time.Duration(0)
	if o.WindowMinutes != nil {
		length = time.Duration(*o.WindowMinutes) * time.Minute
	}
	end = now
	if o.ResetsAt != nil {
		start = o.ResetsAt.Add(-length)
		if o.ResetsAt.Before(now) {
			end = *o.ResetsAt
		}
		return start, end
	}
	return now.Add(-length), now
}
