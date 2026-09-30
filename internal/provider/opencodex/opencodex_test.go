package opencodex

import (
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

func TestParseAndMatch(t *testing.T) {
	const log = `{"timestamp":1790697870000,"provider":"openai","status":200,"usage":{"inputTokens":67859,"outputTokens":164,"cacheReadInputTokens":67196,"cacheCreationInputTokens":659}}
{"timestamp":1790697880000,"provider":"openai-pfbbbde","status":200,"usage":{"inputTokens":500,"outputTokens":20,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}
{"timestamp":1790697890000,"provider":"openai","status":429,"usage":null}
not json
{"timestamp":1790697895000,"provider":"openai-pfbbbde","status":200,"usage":{"inputTokens":500,"outputTokens":20,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}
`
	records, err := parse(strings.NewReader(log))
	if err != nil || len(records) != 3 {
		t.Fatalf("parse = %d records, %v; want the 3 successful ones", len(records), err)
	}
	at := time.UnixMilli(1790697870000).Add(2 * time.Minute)

	// Clients log only the uncached part of the prompt.
	r, ok := Match(records, at, usage.Tokens{Input: 4, CachedInput: 67196, CacheWrite: 659, Output: 164})
	if !ok || r.Provider != MainChatGPT {
		t.Errorf("Match = %+v, %v; want the main account's record", r, ok)
	}
	if _, ok := Match(records, at.Add(time.Hour), usage.Tokens{Input: 4, CachedInput: 67196, CacheWrite: 659, Output: 164}); ok {
		t.Error("a record an hour away must not match")
	}
	// Two records fit equally well: which account served it is unknown.
	if _, ok := Match(records, at, usage.Tokens{Input: 500, Output: 20}); ok {
		t.Error("an ambiguous request must not match")
	}
}
