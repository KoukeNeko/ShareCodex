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
