package quota

import (
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestLatestKeepsNewestPerBucket(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	snapshots := []Snapshot{
		{DeviceID: "laptop", ObservedAt: t0.Add(time.Hour), Buckets: []Bucket{
			{Key: BucketFiveHour, UsedPercent: ptr(40.0)},
		}},
		{DeviceID: "desktop", ObservedAt: t0, Buckets: []Bucket{
			{Key: BucketFiveHour, UsedPercent: ptr(10.0)},
			{Key: BucketWeekly, UsedPercent: ptr(80.0)},
		}},
	}

	got := Latest(snapshots)

	if p := *got[BucketFiveHour].UsedPercent; p != 40 {
		t.Errorf("five_hour = %v, want 40 from the newer snapshot", p)
	}
	if p := *got[BucketWeekly].UsedPercent; p != 80 {
		t.Errorf("weekly = %v, want 80 (only reported once)", p)
	}
}

func TestHasReset(t *testing.T) {
	resets := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	o := Observed{Bucket: Bucket{Key: BucketFiveHour, ResetsAt: &resets}}

	if o.HasReset(resets.Add(-time.Second)) {
		t.Error("window should still be open before resets_at")
	}
	if !o.HasReset(resets) {
		t.Error("window should be reset at resets_at")
	}
	if (Observed{}).HasReset(resets) {
		t.Error("unknown reset time must not be treated as reset")
	}
}

func TestWindow(t *testing.T) {
	resets := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	o := Observed{Bucket: Bucket{ResetsAt: &resets, WindowMinutes: ptr(300)}}
	now := resets.Add(-time.Hour)

	start, end := o.Window(now)
	if !start.Equal(resets.Add(-5*time.Hour)) || !end.Equal(now) {
		t.Errorf("Window = %v..%v", start, end)
	}
}

// A statusLine reading carries its session's last figure, so a later but
// lower one of the same window is stale; the provider's own readings are
// current, so the newest wins even when lower, as after a plan upgrade.
func TestLatestTrustsCurrentReadingsOverStaleStatusLine(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	five := 300
	resets := t0.Add(3 * time.Hour)
	reading := func(source Source, at time.Time, used float64, resets time.Time) Snapshot {
		return Snapshot{Source: source, ObservedAt: at, Buckets: []Bucket{{Key: BucketFiveHour, UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &five}}}
	}
	used := func(snaps ...Snapshot) float64 { return *Latest(snaps)[BucketFiveHour].UsedPercent }

	fresh := reading(SourceClaudeOAuthUsage, t0, 90, resets.Add(-400*time.Millisecond))
	if got := used(fresh, reading(SourceClaudeStatusLine, t0.Add(time.Second), 41, resets)); got != 90 {
		t.Errorf("stale lower statusLine won: %v, want 90", got)
	}
	if got := used(fresh, reading(SourceClaudeStatusLine, t0.Add(time.Minute), 93, resets)); got != 93 {
		t.Errorf("newer higher statusLine of the same window = %v, want 93", got)
	}
	if got := used(fresh, reading(SourceClaudeStatusLine, t0.Add(time.Minute), 1, resets.Add(5*time.Hour))); got != 1 {
		t.Errorf("statusLine of a later window = %v, want 1", got)
	}
	// An upgrade restarts the window two hours later, at 2%%.
	upgraded := reading(SourceClaudeOAuthUsage, t0.Add(time.Hour), 2, resets.Add(2*time.Hour))
	if got := used(fresh, upgraded); got != 2 {
		t.Errorf("after an upgrade = %v, want 2 from the newest usage reading", got)
	}
	if got := used(fresh, upgraded, reading(SourceClaudeStatusLine, t0.Add(2*time.Hour), 100, resets)); got != 2 {
		t.Errorf("a late statusLine reading of the old window = %v, want 2", got)
	}
}

func TestInFamily(t *testing.T) {
	for _, tt := range []struct {
		model, family string
		want          bool
	}{
		{"claude-fable-5-1", "fable", true},
		{"claude-fable-5", "fable", true},
		{"claude-opus-5-5", "fable", false},
		{"ocx-claude-fable-x", "fable", false},
		// Antigravity's pools: Gemini models in one, other vendors' in another.
		{"gemini-3.8-flash", "gemini", true},
		{"claude-sonnet-4-6", "claude", true},
		{"gpt-oss-120b-medium", "claude", true},
		{"gemini-3.8-flash", "claude", false},
		{"claude-sonnet-4-6", "gemini", false},
		{"gpt-oss-120b-medium", "gemini", false},
	} {
		if got := InFamily(tt.model, tt.family); got != tt.want {
			t.Errorf("InFamily(%q, %q) = %v, want %v", tt.model, tt.family, got, tt.want)
		}
	}
}
