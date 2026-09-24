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
