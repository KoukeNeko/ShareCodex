package transcript

import (
	"os"
	"strings"
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/account"
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

// OpenCodex's "native" service is the ChatGPT subscription, so a Claude
// client's request to it is ChatGPT usage under the model's own name; its
// other services stay third-party.
func TestParseOpenCodexNativeIsChatGPTUsage(t *testing.T) {
	line := func(id, model string) string {
		return `{"type":"assistant","sessionId":"s","requestId":"","timestamp":"2026-09-30T10:00:00Z","entrypoint":"claude-desktop",` +
			`"message":{"id":"` + id + `","model":"` + model + `","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	}
	res, err := Parse(strings.NewReader(
		line("msg_e84c4c53aab747968be1ca8434691de9", "ocx-claude-native--gpt-6-sol") +
			line("msg_04d13bbb22d4432e9ea9d2c167e3e775", "ocx-claude-ollama-cloud--deepseek-v4.1-flash")))
	if err != nil || len(res.Events) != 2 {
		t.Fatalf("Parse = %+v, %v", res, err)
	}
	native, routed := res.Events[0], res.Events[1]
	if native.Provider != account.ProviderOpenAI || native.Model != "gpt-6-sol" || native.ThirdParty || native.Gateway != usage.GatewayOpenCodex {
		t.Errorf("native = %+v, want ChatGPT usage of gpt-6-sol through OpenCodex", native)
	}
	if routed.Provider != account.ProviderAnthropic || !routed.ThirdParty || routed.Gateway != usage.GatewayOpenCodex {
		t.Errorf("routed = %+v, want third-party usage on the Claude account", routed)
	}
}

// Every Anthropic model family draws on a Claude subscription; OpenCodex's
// claude-ocx- names and other vendors' models do not.
func TestAnthropicModel(t *testing.T) {
	for model, official := range map[string]bool{
		"claude-opus-5-5":                              true,
		"claude-sonnet-5":                              true,
		"claude-haiku-4-5-20251001":                    true,
		"claude-fable-5-1":                             true,
		"claude-opus-4-8-p05d":                         true,
		"claude-ocx-ollama-cloud--deepseek-v4.1-flash": false,
		"ocx-claude-native--gpt-6-sol":                 false,
		"deepseek-v4.1-flash":                          false,
		"<synthetic>":                                  false,
	} {
		third := !anthropicModel.MatchString(model) || openCodexModel.MatchString(model)
		if third == official {
			t.Errorf("%s: third party = %v, want %v", model, third, !official)
		}
	}
}
