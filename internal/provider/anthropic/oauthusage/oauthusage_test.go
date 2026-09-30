package oauthusage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

const sample = `{"five_hour":{"utilization":23.5,"resets_at":"2026-09-27T22:00:00.146239+00:00"},
 "seven_day":{"utilization":41.2,"resets_at":"2026-10-04T07:00:00+00:00"}}`

const signedIn = `{"claudeAiOauth":{"accessToken":"tok","expiresAt":1790537052000}}`

func TestParseCredential(t *testing.T) {
	t.Run("signed in", func(t *testing.T) {
		c, err := parseCredential([]byte(signedIn))
		if err != nil {
			t.Fatal(err)
		}
		if c.Token != "tok" || !c.ExpiresAt.Equal(time.UnixMilli(1790537052000).UTC()) {
			t.Errorf("got %+v", c)
		}
	})
	for _, raw := range []string{
		`{"mcpOAuth":{"server":{"accessToken":""}}}`,
		`{"claudeAiOauth":{"expiresAt":1790537052000}}`,
	} {
		if _, err := parseCredential([]byte(raw)); !errors.Is(err, ErrNoCredential) {
			t.Errorf("parseCredential(%s) err = %v, want ErrNoCredential", raw, err)
		}
	}
	if _, err := parseCredential([]byte("nope")); err == nil || errors.Is(err, ErrNoCredential) {
		t.Errorf("err = %v, want a decode error", err)
	}
}

func TestPlanWithTier(t *testing.T) {
	for _, tc := range []struct{ plan, tier, want string }{
		{"max", "default_claude_max_20x", "max 20x"},
		{"max", "default_claude_max_5x", "max 5x"},
		{"max", "", "max"},
		{"pro", "default_claude_ai", "pro"},
		{"", "default_claude_max_5x", ""},
	} {
		if got := PlanWithTier(tc.plan, tc.tier); got != tc.want {
			t.Errorf("PlanWithTier(%q, %q) = %q, want %q", tc.plan, tc.tier, got, tc.want)
		}
	}
}

func TestReadCredentialUsesConfigDirProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if _, err := readCredential(context.Background()); !errors.Is(err, ErrNoCredential) {
		t.Errorf("err = %v, want ErrNoCredential for a profile without a credential", err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(signedIn), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := readCredential(context.Background())
	if err != nil || c.Token != "tok" {
		t.Fatalf("readCredential = %+v, %v; want the profile's own credential", c, err)
	}
}

func TestProbeSkipsExpiredCredential(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	raw := `{"claudeAiOauth":{"accessToken":"tok","expiresAt":1000}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	// An expired credential must not reach the endpoint: Claude Code's own
	// supervisor renews it, and this probe only reads what is there.
	got, _, err := Probe(context.Background(), time.Unix(2000, 0))
	if err != nil || len(got) != 0 {
		t.Errorf("Probe = %v, %v; want nothing to read", got, err)
	}
}

func TestGetUsage(t *testing.T) {
	var gotAuth, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAgent = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/usage":
			w.Write([]byte(sample))
		case "/throttled":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := getUsage(context.Background(), srv.URL+"/usage", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || len(gotAgent) < len("claude-cli/") || gotAgent[:len("claude-cli/")] != "claude-cli/" {
		t.Errorf("request sent Authorization %q and User-Agent %q", gotAuth, gotAgent)
	}

	latest := quota.Latest([]quota.Snapshot{{Buckets: got}})
	five, weekly := latest[quota.BucketFiveHour], latest[quota.BucketWeekly]
	if five.UsedPercent == nil || *five.UsedPercent != 23.5 || weekly.UsedPercent == nil || *weekly.UsedPercent != 41.2 {
		t.Fatalf("buckets = %+v", got)
	}
	if five.WindowMinutes == nil || *five.WindowMinutes != quota.FiveHourMinutes ||
		weekly.WindowMinutes == nil || *weekly.WindowMinutes != quota.WeeklyMinutes {
		t.Errorf("window lengths = %v, %v", five.WindowMinutes, weekly.WindowMinutes)
	}
	want := time.Date(2026, 10, 4, 7, 0, 0, 0, time.UTC)
	if weekly.ResetsAt == nil || !weekly.ResetsAt.Equal(want) {
		t.Errorf("weekly resets at %v, want %v", weekly.ResetsAt, want)
	}

	if _, err := getUsage(context.Background(), srv.URL+"/throttled", "tok"); err == nil {
		t.Error("a throttled probe must be reported so the last reading stays")
	}
}

func TestBucketsOmitWhatTheAccountDoesNotReport(t *testing.T) {
	util := 12.0
	got := buckets(usage{SevenDay: &window{Utilization: &util, ResetsAt: "later"}})
	if len(got) != 1 || got[0].Key != quota.BucketWeekly {
		t.Fatalf("buckets = %+v", got)
	}
	if got[0].ResetsAt != nil {
		t.Error("an unparseable reset time must leave the bucket without one")
	}
}

// A throttled or refused read says how long to wait, from Retry-After or an
// hour when Anthropic names none.
func TestGetUsageReportsHowLongToWait(t *testing.T) {
	for _, tc := range []struct {
		status     int
		retryAfter string
		want       time.Duration
	}{
		{http.StatusTooManyRequests, "3588", 3588 * time.Second},
		{http.StatusTooManyRequests, "", time.Hour},
		{http.StatusForbidden, "", time.Hour},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			w.WriteHeader(tc.status)
		}))
		_, err := getUsage(context.Background(), srv.URL, "tok")
		srv.Close()
		var throttled *ThrottledError
		if !errors.As(err, &throttled) || throttled.RetryAfter != tc.want {
			t.Errorf("%d with Retry-After %q: err = %v, want a wait of %s", tc.status, tc.retryAfter, err, tc.want)
		}
	}
}

// A weekly limit on one model family arrives in "limits" and becomes its
// own bucket, keyed by the family.
func TestBucketsIncludeWeeklyModelLimits(t *testing.T) {
	var u usage
	raw := `{"five_hour":{"utilization":2,"resets_at":"2026-09-30T20:30:00+00:00"},
	 "seven_day":{"utilization":58,"resets_at":"2026-10-04T09:00:00+00:00"},
	 "limits":[{"kind":"session","percent":2,"resets_at":"2026-09-30T20:30:00+00:00","scope":null},
	  {"kind":"weekly_scoped","percent":19,"resets_at":"2026-10-04T09:00:00.1+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null}}]}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	got := map[quota.BucketKey]float64{}
	for _, b := range buckets(u) {
		got[b.Key] = *b.UsedPercent
		if b.Key == "weekly_fable" && (b.WindowMinutes == nil || *b.WindowMinutes != quota.WeeklyMinutes || b.ResetsAt == nil) {
			t.Errorf("weekly_fable = %+v, want a weekly window with its reset time", b)
		}
	}
	if len(got) != 3 || got["weekly_fable"] != 19 || got[quota.BucketWeekly] != 58 {
		t.Errorf("buckets = %v, want five_hour, weekly and weekly_fable at 19", got)
	}
	if quota.BucketKey("weekly_fable").ModelFamily() != "fable" || quota.BucketWeekly.ModelFamily() != "" {
		t.Error("only weekly_fable limits a model family")
	}
}
