package transcript

import (
	"os"
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

func TestParseFixture(t *testing.T) {
	f, err := os.Open("../../../../testdata/claude/projects/-demo/s1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	res, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Events) != 2 {
		t.Fatalf("got %d events, want 2 (split lines deduped, synthetic and gateway models skipped)", len(res.Events))
	}
	first := res.Events[0]
	if first.DedupeKey != "claude:msg_1:req_1" || first.Model != "claude-opus-5-5" || first.Originator != "cli" {
		t.Errorf("unexpected first event: %+v", first)
	}
	want := usage.Tokens{Input: 10, CachedInput: 5000, CacheWrite: 300, Output: 200}
	if first.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", first.Tokens, want)
	}
}
