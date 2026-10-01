package antigravity

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

// The figures come from a real reply: Gemini models share one pool that was
// in use, the other vendors' models another that had not started.
const sampleModels = `{"models": {
	"gemini-3.8-flash-tiered": {"quotaInfo": {"remainingFraction": 0.9899, "resetTime": "2026-10-01T14:16:51Z"}},
	"gemini-3.1-pro-high": {"displayName": "Gemini 3.1 Pro (High)", "quotaInfo": {"remainingFraction": 0.9899, "resetTime": "2026-10-01T14:16:51Z"}},
	"claude-opus-4-6-thinking": {"quotaInfo": {"remainingFraction": 1, "resetTime": "2026-10-01T14:35:15Z"}},
	"gpt-oss-120b-medium": {"quotaInfo": {"remainingFraction": 1, "resetTime": "2026-10-01T14:35:15Z"}},
	"chat_23310": {"quotaInfo": {"remainingFraction": 1}},
	"tab_flash_lite_preview": {"quotaInfo": {"remainingFraction": 1}}
}}`

func TestQuotaPools(t *testing.T) {
	var r modelsResponse
	if err := json.Unmarshal([]byte(sampleModels), &r); err != nil {
		t.Fatal(err)
	}
	got := map[quota.BucketKey]quota.Bucket{}
	for _, b := range r.buckets() {
		got[b.Key] = b
	}
	if len(got) != 2 {
		t.Fatalf("buckets = %+v, want one per pool", got)
	}
	gemini, claude := got["five_hour_gemini"], got["five_hour_claude"]
	if gemini.UsedPercent == nil || math.Abs(*gemini.UsedPercent-1.01) > 1e-9 || !gemini.ResetsAt.Equal(time.Date(2026, 10, 1, 14, 16, 51, 0, time.UTC)) || *gemini.WindowMinutes != 300 {
		t.Errorf("gemini = %v %v %v", *gemini.UsedPercent, gemini.ResetsAt, *gemini.WindowMinutes)
	}
	if claude.UsedPercent == nil || *claude.UsedPercent != 0 || !claude.ResetsAt.Equal(time.Date(2026, 10, 1, 14, 35, 15, 0, time.UTC)) {
		t.Errorf("claude = %v %v", *claude.UsedPercent, claude.ResetsAt)
	}
	if got := quota.BucketKey("five_hour_gemini").ModelFamily(); got != "gemini" {
		t.Errorf("family of five_hour_gemini = %q", got)
	}
}

func TestDecodeKeyring(t *testing.T) {
	plain := `{"token":{"access_token":"x"}}`
	if got := decodeKeyring("go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(plain)) + "\n"); got != plain {
		t.Errorf("encoded value = %q", got)
	}
	if got := decodeKeyring(plain); got != plain {
		t.Errorf("plain value = %q", got)
	}
}
