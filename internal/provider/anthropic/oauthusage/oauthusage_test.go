package oauthusage

import (
	"context"
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
