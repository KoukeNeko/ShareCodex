package admin

import (
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

func TestCompactTokens(t *testing.T) {
	tests := map[int64]string{950: "950", 12_345: "12.3K", 4_500_000: "4.5M", 2_100_000_000: "2.1B"}
	for n, want := range tests {
		if got := compactTokens(n); got != want {
			t.Errorf("compactTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDictionariesHaveTheSameKeys(t *testing.T) {
	en := dictionaries[defaultLang]
	for lang, dict := range dictionaries {
		for key := range en {
			if dict[key] == "" {
				t.Errorf("%s is missing %q", lang, key)
			}
		}
		for key := range dict {
			if _, ok := en[key]; !ok {
				t.Errorf("%s has %q, which English lacks", lang, key)
			}
		}
	}
}

func TestModelName(t *testing.T) {
	tests := []struct{ id, gateway, name, via string }{
		{"claude-opus-5-5", "", "claude-opus-5-5", ""},
		{"ocx-claude-native--gpt-6-sol", "", "gpt-6-sol", "OCX"},
		{"native/gpt-6-luna", "opencodex", "gpt-6-luna", "OCX"},
		{"qwen4", "ollama", "qwen4", "Ollama"},
		{"native/gpt-6-luna", "", "native/gpt-6-luna", ""},
	}
	for _, tt := range tests {
		if name, via, _ := modelName(tt.id, tt.gateway); name != tt.name || via != tt.via {
			t.Errorf("modelName(%q, %q) = %q, %q; want %q, %q", tt.id, tt.gateway, name, via, tt.name, tt.via)
		}
	}
}

func TestBucketNameOfAModelFamily(t *testing.T) {
	en := dictionaries[defaultLang]
	if got := bucketName("weekly_fable", 10080, en); got != "Weekly · Fable" {
		t.Errorf("weekly_fable = %q, want Weekly · Fable", got)
	}
	if got := bucketName("other", 120, en); got != "2 hours" {
		t.Errorf("a 2-hour window = %q", got)
	}
}

// The chart is a running total per model that starts again at a reset,
// stops at now, and draws the quota's models before third-party ones in
// their legends' order.
func TestChartRunsTotalsUntilNow(t *testing.T) {
	start := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	tl := syncapi.Timeline{
		Start: start, BinMinutes: 1, Bins: 300,
		Resets: []time.Time{start.Add(100 * time.Minute)},
		Points: []syncapi.TimelinePoint{
			{Bin: 10, Model: "ocx-claude-native--gpt-6-sol", ThirdParty: true, Gateway: "opencodex", Tokens: 7},
			{Bin: 10, Model: "sonnet", Tokens: 50},
			{Bin: 20, Model: "opus", Tokens: 100},
			{Bin: 150, Model: "opus", Tokens: 30},
			{Bin: 290, Model: "opus", Tokens: 999}, // after now
		},
	}
	now := start.Add(200 * time.Minute)
	c := newChart(tl, []string{modelKey("opus", ""), modelKey("sonnet", "")}, []string{modelKey("ocx-claude-native--gpt-6-sol", "opencodex")}, now)

	// 300 one-minute bins in points of 5; the last holds now.
	if len(c.Points) != 41 {
		t.Fatalf("points = %d, want 41 up to now", len(c.Points))
	}
	if len(c.Lines) != 3 || c.Lines[0].Slot != "1" || c.Lines[1].Slot != "2" || !c.Lines[2].Dashed || c.Lines[2].Slot != "1" {
		t.Fatalf("lines = %+v, want opus, sonnet, then the dashed third-party model", c.Lines)
	}
	// Opus reaches 100 before the reset and only 30 after it; the bin after
	// now is not drawn.
	if c.Top != 100 {
		t.Errorf("top = %d, want 100", c.Top)
	}
	// Sonnet and the third-party model started again from zero.
	if got := c.Points[len(c.Points)-1].Lines; len(got) != 1 || got[0] != "30  opus" {
		t.Errorf("last point = %q, want only opus, at 30 after the reset", got)
	}
	if got := c.Points[3].Lines; len(got) != 2 || got[1] != "7  gpt-6-sol (OCX)" {
		t.Errorf("point 3 = %q, want sonnet and the third-party model", got)
	}
	if len(c.Resets) != 1 || c.Resets[0].X < 33.3 || c.Resets[0].X > 33.4 {
		t.Errorf("resets = %+v, want one a third of the way across", c.Resets)
	}
}
