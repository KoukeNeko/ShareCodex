package query

import (
	"math"
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

func TestBucketOverviewAfterResetHasNoModels(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	resets := now.Add(-time.Minute)
	used, window := 60.0, 300
	b := quota.Observed{Bucket: quota.Bucket{Key: quota.BucketFiveHour, UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &window}}
	rows := []storage.UsageRow{{PersonID: "a", Model: "claude-opus-5-5", Tokens: usage.Tokens{Input: 10}, Requests: 1}}

	bo := bucketOverview(b, now, nil, rows, "a")
	if !bo.Reset || len(bo.Models) != 0 {
		t.Errorf("reset bucket = %+v, want no models", bo)
	}
}
