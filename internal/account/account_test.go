package account

import (
	"testing"
	"time"
)

func TestResolveAt(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	obs := []Observation{
		{Provider: ProviderOpenAI, ExternalRefHash: "b", ObservedAt: base.Add(2 * time.Hour)},
		{Provider: ProviderOpenAI, ExternalRefHash: "a", ObservedAt: base},
		{Provider: ProviderAnthropic, ExternalRefHash: "claude", ObservedAt: base.Add(-time.Hour)},
	}

	tests := []struct {
		name   string
		at     time.Time
		want   string
		wantOK bool
	}{
		{"before first observation", base.Add(-time.Minute), "", false},
		{"exactly at observation", base, "a", true},
		{"between observations", base.Add(time.Hour), "a", true},
		{"after account switch", base.Add(3 * time.Hour), "b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveAt(obs, ProviderOpenAI, tt.at)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("ResolveAt() = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestHashExternalRefIsProviderScoped(t *testing.T) {
	if HashExternalRef(ProviderAnthropic, "x@example.com") == HashExternalRef(ProviderOpenAI, "x@example.com") {
		t.Fatal("same email under different providers must not collide")
	}
}

func TestIsMasked(t *testing.T) {
	for _, masked := range []string{"so***@example.com", "a***@example.com", "or***", "***"} {
		if !IsMasked(masked) {
			t.Errorf("IsMasked(%q) = false, want true", masked)
		}
	}
	for _, named := range []string{"", "Team Max", "someone@example.com", "work ***", "Lab ***@x"} {
		if IsMasked(named) {
			t.Errorf("IsMasked(%q) = true, want false", named)
		}
	}
}

func TestResolvePlanAt(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(24 * time.Hour)
	intervals := []PlanInterval{
		{PlanType: "max 5x", EffectiveAt: t0, EndedAt: &t1},
		{PlanType: "max 20x", EffectiveAt: t1, EndedAt: nil},
	}

	if got := ResolvePlanAt(intervals, t0.Add(-time.Hour)); got != "" {
		t.Errorf("before first interval = %q, want empty", got)
	}
	if got := ResolvePlanAt(intervals, t0); got != "max 5x" {
		t.Errorf("at t0 = %q, want max 5x", got)
	}
	if got := ResolvePlanAt(intervals, t0.Add(12*time.Hour)); got != "max 5x" {
		t.Errorf("during first interval = %q, want max 5x", got)
	}
	if got := ResolvePlanAt(intervals, t1); got != "max 20x" {
		t.Errorf("at t1 = %q, want max 20x", got)
	}
	if got := ResolvePlanAt(intervals, t1.Add(48*time.Hour)); got != "max 20x" {
		t.Errorf("after t1 = %q, want max 20x", got)
	}
}

func TestMaskRef(t *testing.T) {
	tests := map[string]string{
		"someone@example.com": "so***@example.com",
		"a@example.com":       "a***@example.com",
		"org-1234":            "or***",
	}
	for in, want := range tests {
		if got := MaskRef(in); got != want {
			t.Errorf("MaskRef(%q) = %q, want %q", in, got, want)
		}
	}
}
