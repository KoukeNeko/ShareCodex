package transcript

import (
	"fmt"
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

func TestParseEffort(t *testing.T) {
	line := func(id, effort string) string {
		return `{"type":"assistant","sessionId":"s","requestId":"r` + id + `","timestamp":"2026-09-30T10:00:00Z",` + effort +
			`"message":{"id":"m` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
	}
	res, err := Parse(strings.NewReader(
		line("1", `"perTurnEffort":"xhigh",`) + line("2", `"perTurnEffort":"High",`) + line("3", `"perTurnEffort":null,`) + line("4", "")))
	if err != nil || len(res.Events) != 4 {
		t.Fatalf("Parse = %+v, %v", res, err)
	}
	for i, want := range []string{"xhigh", "high", "", ""} {
		if got := res.Events[i].Effort; got != want {
			t.Errorf("event %d effort = %q, want %q", i, got, want)
		}
	}
}

func errorLine(uuid string, fields, text string) string {
	return `{"type":"assistant","isApiErrorMessage":true,"uuid":"` + uuid + `","sessionId":"s","requestId":"req","timestamp":"2026-09-30T10:00:00Z",` +
		`"entrypoint":"cli",` + fields + `"message":{"id":"x","model":"<synthetic>","content":[{"type":"text","text":"` + text + `"}],` +
		`"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n"
}

func TestParseLimitEvents(t *testing.T) {
	for _, tc := range []struct {
		name, fields, text string
		kind               usage.LimitKind
		evidence           string
		status             int
	}{
		{"five-hour quota", `"error":"rate_limit","apiErrorStatus":429,"quotaLimits":{"status":"rejected","rateLimitType":"five_hour"},`,
			"You've hit your session limit", usage.LimitFiveHour, "five_hour", 429},
		{"weekly quota", `"error":"rate_limit","quotaLimits":{"rateLimitType":"seven_day"},`, "x", usage.LimitWeekly, "seven_day", 0},
		{"model quota", `"error":"rate_limit","quotaLimits":{"rateLimitType":"seven_day_opus"},`, "x", usage.LimitModelSpecific, "seven_day_opus", 0},
		{"other quota", `"error":"rate_limit","quotaLimits":{"rateLimitType":"daily"},`, "x", usage.LimitUnknown, "daily", 0},
		{"old session limit", `"error":"rate_limit",`, "You've hit your session limit · resets 5:30pm (Asia/Taipei)", usage.LimitFiveHour, "session_limit", 0},
		{"curly apostrophe", `"error":"rate_limit",`, "You’ve hit your session limit", usage.LimitFiveHour, "session_limit", 0},
		{"old weekly limit", `"error":"rate_limit",`, "You've hit your weekly limit · resets Mon", usage.LimitWeekly, "weekly_limit", 0},
		{"monthly spend", `"error":"rate_limit",`, "You've hit your monthly spend limit", usage.LimitUnknown, "extra_usage", 0},
		{"extra usage", `"error":"rate_limit",`, "You're out of extra usage · resets 3am", usage.LimitUnknown, "extra_usage", 0},
		{"gateway throttle", `"error":"rate_limit",`, "API Error: Server is temporarily limiting requests (not your usage limit)", usage.LimitProvider429, "gateway", 0},
		{"proxy 429", `"error":"rate_limit","apiErrorStatus":429,`, "API Error: Request rejected (429) · All Anthropic OAuth accounts are rate-limited", usage.LimitProvider429, "gateway", 429},
		{"overload", `"error":"server_error","apiErrorStatus":529,`, "API Error: 529 Overloaded", usage.LimitOverload, "overloaded", 529},
		{"authentication", `"error":"authentication_failed","apiErrorStatus":403,`, "Please run /login", usage.LimitAuth, "auth", 403},
	} {
		res, err := Parse(strings.NewReader(errorLine("u1", tc.fields, tc.text)))
		if err != nil || len(res.LimitEvents) != 1 || len(res.Events) != 0 {
			t.Errorf("%s: Parse = %+v, %v", tc.name, res, err)
			continue
		}
		l := res.LimitEvents[0]
		if l.Kind != tc.kind || l.Evidence != tc.evidence || l.HTTPStatus != tc.status {
			t.Errorf("%s: got %s/%s/%d, want %s/%s/%d", tc.name, l.Kind, l.Evidence, l.HTTPStatus, tc.kind, tc.evidence, tc.status)
		}
		if l.DedupeKey != "claude-limit:u1" || l.Source != LimitSource || l.SessionID != "s" || l.RequestID != "req" ||
			l.Originator != "cli" || l.Provider != account.ProviderAnthropic || l.OccurredAt.Format("2006-01-02T15:04:05Z") != "2026-09-30T10:00:00Z" {
			t.Errorf("%s: event = %+v", tc.name, l)
		}
	}
}

// Failures that say nothing about limits are not recorded.
func TestParseSkipsErrorNoise(t *testing.T) {
	res, err := Parse(strings.NewReader(
		errorLine("u1", `"error":"server_error",`, "API Error: Can't reach the API server (ENOTFOUND)") +
			errorLine("u2", `"error":"unknown","apiErrorStatus":405,`, "API Error: 405 status code (no body)") +
			errorLine("u3", `"error":"invalid_request","apiErrorStatus":400,`, "API Error: 400") +
			errorLine("u4", `"error":"rate_limit",`, "Something else") +
			errorLine("", `"error":"rate_limit","quotaLimits":{"rateLimitType":"five_hour"},`, "no uuid")))
	if err != nil || len(res.LimitEvents) != 0 || len(res.Events) != 0 {
		t.Fatalf("Parse = %+v, %v; want nothing recorded", res, err)
	}
}

func TestLimitEventKeepsNoMessageText(t *testing.T) {
	res, err := Parse(strings.NewReader(errorLine("u1", `"error":"rate_limit",`, "You've hit your session limit · resets 5:30pm (Asia/Taipei)")))
	if err != nil || len(res.LimitEvents) != 1 {
		t.Fatalf("Parse = %+v, %v", res, err)
	}
	if got := fmt.Sprintf("%+v", res.LimitEvents[0]); strings.Contains(got, "Taipei") || strings.Contains(got, "resets") {
		t.Errorf("event %s holds message text", got)
	}
}
