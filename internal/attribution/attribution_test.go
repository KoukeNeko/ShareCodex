package attribution

import (
	"math"
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestApportion(t *testing.T) {
	shares, unattributed := Apportion(60,
		map[string]float64{"a": 3, "b": 1},
		map[string]float64{"a": 1, "b": 1, "c": 2},
	)
	if unattributed != 0 {
		t.Errorf("unattributed = %v, want 0", unattributed)
	}
	checks := map[string]Share{
		"a": {AllottedPercent: 25, UsedPercent: 45},
		"b": {AllottedPercent: 25, UsedPercent: 15},
		"c": {AllottedPercent: 50, UsedPercent: 0},
	}
	for p, want := range checks {
		got := shares[p]
		if !near(got.AllottedPercent, want.AllottedPercent) || !near(got.UsedPercent, want.UsedPercent) {
			t.Errorf("%s = %+v, want %+v", p, got, want)
		}
	}
}

func TestApportionWithoutEventsIsUnattributed(t *testing.T) {
	shares, unattributed := Apportion(30, nil, map[string]float64{"a": 1})
	if unattributed != 30 {
		t.Errorf("unattributed = %v, want 30", unattributed)
	}
	if shares["a"].UsedPercent != 0 || shares["a"].AllottedPercent != 100 {
		t.Errorf("a = %+v", shares["a"])
	}
}

func TestApportionZeroWeightMemberGetsNoAllotment(t *testing.T) {
	shares, _ := Apportion(10, map[string]float64{"a": 1, "b": 1}, map[string]float64{"a": 1, "b": 0})
	if shares["b"].AllottedPercent != 0 || !near(shares["b"].UsedPercent, 5) {
		t.Errorf("b = %+v; a removed member keeps usage but no allotment", shares["b"])
	}
}

func TestCostWeightsModels(t *testing.T) {
	tok := usage.Tokens{Input: 1_000_000}
	if Cost("claude-opus-5-5", tok) <= Cost("claude-sonnet-5", tok) {
		t.Error("opus should weigh more than sonnet")
	}
	if Cost("gpt-5.5-mini", tok) >= Cost("gpt-5.5", tok) {
		t.Error("mini should weigh less than the full model")
	}
	if Cost("unknown", tok) == 0 {
		t.Error("unknown models need a default weight")
	}
}
