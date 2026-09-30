// Package quota describes how much of an account's subscription allowance
// remains. It is kept separate from usage: usage says what was consumed,
// quota says what the provider reports is left.
package quota

import (
	"sort"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
)

type BucketKey string

const (
	BucketFiveHour BucketKey = "five_hour"
	BucketWeekly   BucketKey = "weekly"
)

// weeklyModelPrefix starts the key of a weekly limit on one model family,
// such as Claude's "weekly_fable" beside the all-models "weekly".
const weeklyModelPrefix = "weekly_"

// WeeklyModelBucket is the key of the weekly limit on one model family.
func WeeklyModelBucket(family string) BucketKey {
	return BucketKey(weeklyModelPrefix + strings.ToLower(family))
}

// ModelFamily is the model family a bucket limits, such as "fable", or ""
// for a limit on all models.
func (k BucketKey) ModelFamily() string {
	family, ok := strings.CutPrefix(string(k), weeklyModelPrefix)
	if !ok {
		return ""
	}
	return family
}

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
// Most sources report the provider's figure as of the moment they read it,
// so the newest reading is the truth, even when it is lower: a plan upgrade
// can restart a window. A statusLine reading is different: it carries the
// figure from its session's last response, which can be long out of date,
// yet it is observed now. It only replaces the current value when it is
// newer and either reports more usage of the same window or a later window.
func Latest(snapshots []Snapshot) map[BucketKey]Observed {
	latest := make(map[BucketKey]Observed)
	var stale []Observed
	for _, s := range snapshots {
		for _, b := range s.Buckets {
			o := Observed{Bucket: b, ObservedAt: s.ObservedAt}
			if s.Source == SourceClaudeStatusLine {
				stale = append(stale, o)
				continue
			}
			if cur, ok := latest[b.Key]; !ok || o.ObservedAt.After(cur.ObservedAt) {
				latest[b.Key] = o
			}
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].ObservedAt.Before(stale[j].ObservedAt) })
	for _, o := range stale {
		if cur, ok := latest[o.Key]; !ok || statusLineReplaces(o, cur) {
			latest[o.Key] = o
		}
	}
	return latest
}

// sameWindowSlack absorbs the jitter in a window's reported reset time.
const sameWindowSlack = time.Minute

func statusLineReplaces(next, cur Observed) bool {
	if !next.ObservedAt.After(cur.ObservedAt) {
		return false
	}
	if next.ResetsAt == nil || cur.ResetsAt == nil || next.UsedPercent == nil || cur.UsedPercent == nil {
		return true
	}
	switch d := next.ResetsAt.Sub(*cur.ResetsAt); {
	case d > sameWindowSlack:
		return true // a later window
	case d < -sameWindowSlack:
		return false // an earlier window
	}
	return *next.UsedPercent > *cur.UsedPercent
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
