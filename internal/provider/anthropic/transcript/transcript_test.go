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

	if len(res.Events) != 3 {
		t.Fatalf("got %d events, want 3 (split lines deduped, synthetic skipped)", len(res.Events))
	}
	for _, e := range res.Events {
		// Only another vendor's model, reached through a gateway, is
		// third-party.
		if want := e.DedupeKey == "claude:msg_9:req_9"; e.ThirdParty != want {
			t.Errorf("%s (%s): third party = %v, want %v", e.DedupeKey, e.Model, e.ThirdParty, want)
		}
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

// Gateways are told apart by the message IDs they answer with, and OpenCodex
// also by its model names; anything else is left unnamed.
func TestGateway(t *testing.T) {
	for _, tc := range []struct{ name, model, messageID, requestID, want string }{
		{"Anthropic", "claude-opus-5-5", "msg_011CfZUPYx84N8rqh6cnxade", "req_011CfZUPWtqjm84Ah7j9", ""},
		{"OpenCodex model name", "ocx-claude-ollama-cloud--deepseek-v4.1-flash", "msg_e84c4c53aab747968be1ca8434691de9", "", usage.GatewayOpenCodex},
		{"older OpenCodex model name", "claude-ocx-ollama-cloud--deepseek-v4.1-flash", "msg_x", "", usage.GatewayOpenCodex},
		{"OpenCodex message ID", "deepseek-v4-pro", "msg_04d13bbb22d4432e9ea9d2c167e3e775", "", usage.GatewayOpenCodex},
		{"Ollama", "deepseek-v4.1-flash", "msg_ca2f9a1fae5d277139c0d02c", "", usage.GatewayOllama},
		{"unknown gateway", "glm-5.3-flash", "chatcmpl-123", "", ""},
		{"a request ID means no known gateway", "deepseek-v4.1-flash", "msg_ca2f9a1fae5d277139c0d02c", "req_1", ""},
	} {
		if got := gateway(tc.model, tc.messageID, tc.requestID); got != tc.want {
			t.Errorf("%s: gateway = %q, want %q", tc.name, got, tc.want)
		}
	}
}
