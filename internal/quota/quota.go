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
	DeviceID       string
	Provider       account.Provider
	ObservedAt     time.Time
	Source         Source
	Buckets        []Bucket
}

// Observed is a bucket together with when it was reported.
type Observed struct {
	Bucket
	ObservedAt time.Time
}

// Latest merges snapshots reported by any device for one account, keeping the
// most recently observed value of each bucket.
func Latest(snapshots []Snapshot) map[BucketKey]Observed {
	latest := make(map[BucketKey]Observed)
	for _, s := range snapshots {
		for _, b := range s.Buckets {
			if cur, ok := latest[b.Key]; ok && !s.ObservedAt.After(cur.ObservedAt) {
				continue
			}
			latest[b.Key] = Observed{Bucket: b, ObservedAt: s.ObservedAt}
		}
	}
	return latest
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
