package rollout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

const fixtureDir = "../../../../../testdata/codex/sessions/2026/09/20"

func parseFixture(t *testing.T, name string) Result {
	t.Helper()
	f, err := os.Open(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestParsePerRequestRecords(t *testing.T) {
	res := parseFixture(t, "rollout-2026-09-20T10-00-00-aaaa.jsonl")

	if len(res.Events) != 2 {
		t.Fatalf("got %d events, want 2 (token_count must not be double counted; partial line ignored)", len(res.Events))
	}
	e := res.Events[0]
	if e.DedupeKey != "codex:resp_1" || e.Model != "gpt-5.5" || e.Originator != "codex-tui" || e.SessionID != "aaaa" {
		t.Errorf("unexpected event: %+v", e)
	}
	want := usage.Tokens{Input: 600, CachedInput: 400, Output: 100, ReasoningOutput: 20}
	if e.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", e.Tokens, want)
	}
	if res.BadLines != 0 {
		t.Errorf("BadLines = %d, want 0", res.BadLines)
	}

	if len(res.Snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(res.Snapshots))
	}
	latest := quota.Latest(res.Snapshots)
	if p := *latest[quota.BucketFiveHour].UsedPercent; p != 12 {
		t.Errorf("five_hour = %v, want 12", p)
	}
	if p := *latest[quota.BucketWeekly].UsedPercent; p != 40 {
		t.Errorf("weekly = %v, want 40", p)
	}
}

func TestParseLegacyCumulativeCounts(t *testing.T) {
	res := parseFixture(t, "rollout-2026-09-20T11-00-00-bbbb.jsonl")

	if len(res.Events) != 2 {
		t.Fatalf("got %d events, want 2 (repeated count skipped)", len(res.Events))
	}
	second := res.Events[1]
	want := usage.Tokens{Input: 200, CachedInput: 1000, Output: 30}
	if second.Tokens != want {
		t.Errorf("delta tokens = %+v, want %+v", second.Tokens, want)
	}
	if second.DedupeKey != "codex:bbbb:tc:1780" {
		t.Errorf("DedupeKey = %q", second.DedupeKey)
	}
}

func TestParseSubAgentIgnoresInheritedTotals(t *testing.T) {
	res := parseFixture(t, "rollout-2026-09-20T12-00-00-cccc.jsonl")

	var total usage.Tokens
	for _, e := range res.Events {
		total = total.Add(e.Tokens)
	}
	want := usage.Tokens{Input: 1700, Output: 300}
	if total != want {
		t.Errorf("total = %+v, want %+v (only the sub-agent's own requests)", total, want)
	}
}

func TestParseSkipsThirdPartyProvider(t *testing.T) {
	res := parseFixture(t, "rollout-2026-09-20T13-00-00-dddd.jsonl")

	if !res.Skipped || len(res.Events) != 0 {
		t.Errorf("third-party provider session must be skipped, got %+v", res)
	}
}

func TestParseIsDeterministic(t *testing.T) {
	a := parseFixture(t, "rollout-2026-09-20T11-00-00-bbbb.jsonl")
	b := parseFixture(t, "rollout-2026-09-20T11-00-00-bbbb.jsonl")
	for i := range a.Events {
		if a.Events[i].DedupeKey != b.Events[i].DedupeKey {
			t.Fatalf("re-parsing produced different keys: %q vs %q", a.Events[i].DedupeKey, b.Events[i].DedupeKey)
		}
	}
}
