package query

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/attribution"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

func TestBucketOverviewBreaksDownModels(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	resets := now.Add(2 * time.Hour)
	used, window := 60.0, 300
	b := quota.Observed{Bucket: quota.Bucket{Key: quota.BucketFiveHour, UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &window}}

	opus := usage.Tokens{Input: 1000, CachedInput: 500, Output: 200}
	sonnet := usage.Tokens{Input: 3000, Output: 100}
	rows := []storage.UsageRow{
		{PersonID: "a", Model: "claude-opus-5-5", Tokens: opus, Requests: 2},
		{PersonID: "b", Model: "claude-opus-5-5", Tokens: opus, Requests: 1},
		{PersonID: "b", Model: "claude-sonnet-5", Tokens: sonnet, Requests: 4},
	}
	members := []storage.Member{{PersonID: "a", Name: "alice", ShareWeight: 1}, {PersonID: "b", Name: "bob", ShareWeight: 1}}

	bo := bucketOverview(b, now, members, rows, "a")

	if len(bo.Models) != 2 {
		t.Fatalf("models = %+v, want 2", bo.Models)
	}
	opusCost := 2 * attribution.Cost("claude-opus-5-5", opus)
	sonnetCost := attribution.Cost("claude-sonnet-5", sonnet)
	top := bo.Models[0]
	if top.Model != "claude-opus-5-5" || top.Requests != 3 || top.Tokens != 2*1700 {
		t.Errorf("top model = %+v", top)
	}
	want := used * opusCost / (opusCost + sonnetCost)
	if math.Abs(top.UsedPercent-want) > 1e-9 {
		t.Errorf("opus share = %v, want %v", top.UsedPercent, want)
	}
	var sum float64
	for _, m := range bo.Models {
		sum += m.UsedPercent
	}
	if math.Abs(sum-used) > 1e-9 {
		t.Errorf("model shares sum to %v, want the bucket's %v", sum, used)
	}

	for _, m := range bo.Members {
		var memberSum float64
		for i, mm := range m.Models {
			memberSum += mm.UsedPercent
			if i > 0 && m.Models[i-1].Model == "claude-sonnet-5" {
				t.Errorf("%s: segments not in the bucket's model order: %+v", m.Name, m.Models)
			}
		}
		if math.Abs(memberSum-m.UsedPercent) > 1e-9 {
			t.Errorf("%s: segments sum to %v, want %v", m.Name, memberSum, m.UsedPercent)
		}
	}
	for _, m := range bo.Members {
		if m.Name == "alice" && (len(m.Models) != 1 || m.Models[0].Model != "claude-opus-5-5") {
			t.Errorf("alice used only opus, got %+v", m.Models)
		}
	}
}

// One person's usage splits by device, most used first, and adds up to
// their share.
func TestBucketOverviewSplitsMembersByDevice(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	resets := now.Add(2 * time.Hour)
	used, window := 40.0, 300
	b := quota.Observed{Bucket: quota.Bucket{Key: quota.BucketFiveHour, UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &window}}
	tok := usage.Tokens{Input: 1000, Output: 100}
	rows := []storage.UsageRow{
		{PersonID: "a", DeviceName: "mac", Model: "claude-opus-5-5", Tokens: tok, Requests: 1},
		{PersonID: "a", DeviceName: "linux", Model: "claude-opus-5-5", Tokens: tok, Requests: 1},
		{PersonID: "a", DeviceName: "linux", Model: "claude-opus-5-5", Tokens: tok, Requests: 1},
		{PersonID: "b", DeviceName: "pc", Model: "claude-opus-5-5", Tokens: tok, Requests: 1},
	}
	members := []storage.Member{{PersonID: "a", Name: "alice", ShareWeight: 1}, {PersonID: "b", Name: "bob", ShareWeight: 1}}

	bo := bucketOverview(b, now, members, rows, "a")
	for _, m := range bo.Members {
		if m.Name != "alice" {
			continue
		}
		if len(m.Devices) != 2 || m.Devices[0].Name != "linux" || m.Devices[1].Name != "mac" {
			t.Fatalf("alice's devices = %+v, want linux then mac", m.Devices)
		}
		if math.Abs(m.Devices[0].UsedPercent-20) > 1e-9 || math.Abs(m.Devices[1].UsedPercent-10) > 1e-9 {
			t.Errorf("alice's devices = %+v, want linux 20%%, mac 10%%", m.Devices)
		}
		if math.Abs(m.Devices[0].UsedPercent+m.Devices[1].UsedPercent-m.UsedPercent) > 1e-9 {
			t.Errorf("devices sum to %v, want alice's %v", m.Devices[0].UsedPercent+m.Devices[1].UsedPercent, m.UsedPercent)
		}
	}
}

func TestBucketOverviewAfterResetHasNoModels(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	resets := now.Add(-time.Minute)
	used, window := 60.0, 300
	b := quota.Observed{Bucket: quota.Bucket{Key: quota.BucketFiveHour, UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &window}}
	rows := []storage.UsageRow{{PersonID: "a", Model: "claude-opus-5-5", Tokens: usage.Tokens{Input: 10}, Requests: 1}}

	bo := bucketOverview(b, now, nil, rows, "a")
	if !bo.Reset || bo.UsedPercent != 0 || len(bo.Models) != 0 || bo.UnattributedPercent != 0 {
		t.Errorf("reset bucket = %+v, want no estimated usage", bo)
	}
	for _, m := range bo.Members {
		if m.UsedPercent != 0 || m.Requests != 0 {
			t.Errorf("reset bucket attributed usage to %s: %+v", m.Name, m)
		}
	}
}

func TestGroupActiveUsers(t *testing.T) {
	devices := []storage.ActiveDevice{
		{AccountID: "max", PersonID: "c", PersonName: "carol", DeviceName: "pc"},
		{AccountID: "max", PersonID: "b", PersonName: "bob", DeviceName: "laptop"},
		{AccountID: "max", PersonID: "a", PersonName: "alice", DeviceName: "mac"},
		{AccountID: "max", PersonID: "b", PersonName: "bob", DeviceName: "desktop"},
		// bob's desktop is on max in both the CLI and Claude Desktop.
		{AccountID: "max", PersonID: "b", PersonName: "bob", DeviceName: "desktop"},
		{AccountID: "plus", PersonID: "a", PersonName: "alice", DeviceName: "mac"},
	}

	got := groupActiveUsers(devices, "b")

	max := got["max"]
	if len(max) != 3 || max[0].Name != "bob" || !max[0].IsYou || max[1].Name != "alice" || max[2].Name != "carol" {
		t.Fatalf("max = %+v, want bob (you) first, then alice, carol", max)
	}
	if !slices.Equal(max[0].Devices, []string{"desktop", "laptop"}) {
		t.Errorf("bob's devices = %v, want both once each, sorted", max[0].Devices)
	}
	if plus := got["plus"]; len(plus) != 1 || plus[0].IsYou {
		t.Errorf("plus = %+v, want alice only", plus)
	}
}

// The timeline marks where earlier windows reset. A reading of a window that
// has not started yet reports a reset a full window away, which moves with
// every idle reading; those are not resets.
func TestPastResetsMarksEarlierWindowsInsideTheTimeline(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 25, 0, 0, time.UTC)
	start := now.Add(-5 * time.Hour)
	five := 300
	at := func(h, m, ms int) time.Time {
		return time.Date(2026, 9, 30, h, m, 0, ms*int(time.Millisecond), time.UTC)
	}
	snap := func(observed time.Time, resets *time.Time, key quota.BucketKey) quota.Snapshot {
		return quota.Snapshot{ObservedAt: observed, Buckets: []quota.Bucket{{Key: key, ResetsAt: resets, WindowMinutes: &five}}}
	}
	ptr := func(t time.Time) *time.Time { return &t }
	snaps := []quota.Snapshot{
		snap(at(8, 1, 0), ptr(at(8, 20, 647)), quota.BucketFiveHour),
		snap(at(8, 16, 0), ptr(at(8, 20, 142)), quota.BucketFiveHour), // the same window, read again
		snap(at(8, 21, 0), nil, quota.BucketFiveHour),                 // after the reset, before a new window
		snap(at(1, 20, 0), ptr(at(3, 20, 0)), quota.BucketFiveHour),   // before the timeline starts
		snap(at(8, 0, 0), ptr(at(9, 0, 0)), quota.BucketWeekly),       // another bucket
		// Idle readings: each reports a window that would start then.
		snap(at(0, 43, 0), ptr(at(5, 43, 0)), quota.BucketFiveHour),
		snap(at(0, 46, 0), ptr(at(5, 46, 0)), quota.BucketFiveHour),
	}
	got := pastResets(snaps, quota.BucketFiveHour, start, now, 15*time.Minute)
	if want := []time.Time{at(8, 20, 0)}; !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("pastResets = %v, want %v", got, want)
	}
}

// A few wrong readings, such as statusLine readings from a session on another
// account, must not pass for a reset when many more readings show one window
// running through it, and must not hide the real reset either.
func TestPastResetsIgnoresOutvotedReadings(t *testing.T) {
	week := 10080
	at := func(day, hour int) time.Time { return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC) }
	snap := func(observed, resets time.Time) quota.Snapshot {
		return quota.Snapshot{ObservedAt: observed, Buckets: []quota.Bucket{{Key: quota.BucketWeekly, ResetsAt: &resets, WindowMinutes: &week}}}
	}
	var snaps []quota.Snapshot
	for h := 0; h < 20; h++ {
		snaps = append(snaps, snap(at(25, 17).Add(time.Duration(h)*time.Hour), at(27, 9))) // the real window
	}
	for h := 0; h < 3; h++ {
		snaps = append(snaps, snap(at(26, 19).Add(time.Duration(h)*time.Minute), at(30, 11))) // stray readings
	}
	for h := 0; h < 60; h++ {
		snaps = append(snaps, snap(at(27, 17).Add(time.Duration(h)*time.Hour), time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))) // the next window
	}
	now := at(30, 12)
	got := pastResets(snaps, quota.BucketWeekly, now.Add(-7*24*time.Hour), now, time.Duration(week)*time.Minute/2)
	if want := []time.Time{at(27, 9)}; !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("pastResets = %v, want only the real reset %v", got, want)
	}
}
