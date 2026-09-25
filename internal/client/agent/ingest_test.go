package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/client/storage"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/desktop"
	"github.com/KoukeNeko/ShareCodex/internal/scan"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

func TestIngestFileAttributesByAccountTimeline(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	obs := []account.Observation{
		{Provider: account.ProviderAnthropic, ExternalRefHash: "max", ObservedAt: t0},
		// Signed in with an API key an hour later: not pooled usage.
		{Provider: account.ProviderAnthropic, ExternalRefHash: "", ObservedAt: t0.Add(time.Hour)},
	}
	events := []usage.Event{
		{DedupeKey: "before-join", Provider: account.ProviderAnthropic, OccurredAt: t0.Add(-time.Minute)},
		{DedupeKey: "pooled", Provider: account.ProviderAnthropic, OccurredAt: t0.Add(time.Minute)},
		{DedupeKey: "api-key", Provider: account.ProviderAnthropic, OccurredAt: t0.Add(2 * time.Hour)},
	}
	src := source{
		provider: account.ProviderAnthropic,
		parse:    func(*os.File) (parsed, error) { return parsed{events: events}, nil },
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	ac := accounts{provider: account.ProviderAnthropic, cli: obs, since: t0}
	n, err := a.ingestFile(ctx, src, path, scan.FileState{Size: 0, ModTime: t0}, ac)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ingested %d events, want 1 (only the one on the pooled account)", n)
	}
	items, err := store.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	req, err := batchRequest(items)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Events) != 1 || req.Events[0].DedupeKey != "pooled" || req.Events[0].AccountRefHash != "max" {
		t.Fatalf("queued events = %+v", req.Events)
	}
}

// Claude Desktop signs in separately from the CLI, so its sessions follow
// the organization Desktop ran them under, whatever the CLI is signed into.
func TestResolveSplitsClaudeDesktopFromTheCLI(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	ac := accounts{
		provider:    account.ProviderAnthropic,
		cli:         []account.Observation{{Provider: account.ProviderAnthropic, ExternalRefHash: "cli-max", ObservedAt: t0}},
		desktopSeen: true,
		desktop:     map[string]desktop.Session{"desk": {OrgID: "org-b", LastActivity: t0}},
		since:       t0,
	}
	desk := account.HashExternalRef(account.ProviderAnthropic, "org-b")
	later := t0.Add(time.Hour)

	for _, tc := range []struct {
		name, session, entrypoint string
		at                        time.Time
		want                      string
	}{
		{"CLI session", "cli-session", "cli", later, "cli-max"},
		{"Desktop session", "desk", "claude-desktop", later, desk},
		// Desktop's metadata wins over what the transcript says.
		{"Desktop session logged as cli", "desk", "cli", later, desk},
		{"Desktop session before joining", "desk", "claude-desktop", t0.Add(-time.Minute), ""},
		{"Desktop session without metadata", "gone", "claude-desktop", later, ""},
		{"Desktop with third-party inference", "3p", "claude-desktop-3p", later, ""},
		{"statusLine snapshot of a Desktop session", "desk", "", later, desk},
		{"statusLine snapshot of a CLI session", "cli-session", "", later, "cli-max"},
	} {
		ref, ok := ac.resolve(tc.session, tc.entrypoint, tc.at)
		if (tc.want != "") != ok || ref != tc.want {
			t.Errorf("%s: resolve = %q, %v; want %q", tc.name, ref, ok, tc.want)
		}
	}

	// Without the CLI, Desktop usage is still pooled.
	desktopOnly := accounts{provider: account.ProviderAnthropic, desktopSeen: true, desktop: ac.desktop, since: t0}
	if !desktopOnly.pooled() {
		t.Error("a device using only Claude Desktop must be pooled")
	}
	if ref, ok := desktopOnly.resolve("desk", "claude-desktop", later); !ok || ref != desk {
		t.Errorf("Desktop-only resolve = %q, %v; want %q", ref, ok, desk)
	}
}
