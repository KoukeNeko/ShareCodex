package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/client/storage"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/scan"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
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
		parse:    func(string) (parsed, error) { return parsed{events: events}, nil },
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

func TestIngestStatusLineCorrectsKnownCLIReading(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, dir: dir, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	used, minutes := 42.0, 300
	snap := quota.Snapshot{Provider: account.ProviderAnthropic, AccountRefHash: "wrong", Source: quota.SourceClaudeStatusLine,
		ObservedAt: at, Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used, WindowMinutes: &minutes}}}
	if err := store.AddSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, ExternalRefHash: "correct", ObservedAt: at.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFile(ctx, "s.jsonl", scan.FileState{}, []usage.Event{{DedupeKey: "e", Provider: account.ProviderAnthropic,
		AccountRefHash: "correct", Originator: "cli", SessionID: "s", OccurredAt: at.Add(-time.Second)}}, nil); err != nil {
		t.Fatal(err)
	}
	items, err := store.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteOutboxThrough(ctx, items[len(items)-1].ID); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.SpoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	spool := fmt.Sprintf(`{"session_id":"s","observed_at":%q,"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":0}}}`, at.Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(a.SpoolDir(), "s.json"), []byte(spool), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := a.ingestStatusLine(ctx); err != nil || n != 1 {
		t.Fatalf("ingested statusLine = %d, %v", n, err)
	}
	stored, err := store.LatestSnapshots(ctx, at.Add(-time.Hour))
	if err != nil || len(stored) != 1 || stored[0].AccountRefHash != "correct" {
		t.Fatalf("corrected snapshot = %+v, %v", stored, err)
	}
	items, err = store.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	req, err := batchRequest(items)
	if err != nil || len(req.Snapshots) != 2 || req.Snapshots[0].PreviousAccountRefHash != "wrong" || req.Snapshots[1].AccountRefHash != "correct" {
		t.Fatalf("correction outbox = %+v, %v", req.Snapshots, err)
	}
}

func TestIngestStatusLineDoesNotMoveReadingWithoutDesktopMetadata(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, dir: dir, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Minute)
	used, minutes := 42.0, 300
	snap := quota.Snapshot{Provider: account.ProviderAnthropic, AccountRefHash: "wrong", Source: quota.SourceClaudeStatusLine,
		ObservedAt: at, Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used, WindowMinutes: &minutes}}}
	if err := store.AddSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteOutboxThrough(ctx, 1); err != nil {
		t.Fatal(err)
	}
	events := []usage.Event{
		{DedupeKey: "desk", AccountRefHash: "wrong", Provider: account.ProviderAnthropic, Originator: "claude-desktop", SessionID: "desk", OccurredAt: at.Add(-time.Minute)},
		{DedupeKey: "cli", AccountRefHash: "cli", Provider: account.ProviderAnthropic, Originator: "cli", SessionID: "desk", OccurredAt: at.Add(time.Second)},
	}
	if _, err := store.IngestFile(ctx, "s.jsonl", scan.FileState{}, events, nil); err != nil {
		t.Fatal(err)
	}
	items, err := store.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteOutboxThrough(ctx, items[len(items)-1].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, ExternalRefHash: "cli", ObservedAt: at.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, Source: account.SourceClaudeDesktop,
		ExternalRefHash: account.HashExternalRef(account.ProviderAnthropic, "other-org"), ObservedAt: at.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.SpoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	spool := fmt.Sprintf(`{"session_id":"desk","observed_at":%q,"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":0}}}`, at.Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(a.SpoolDir(), "desk.json"), []byte(spool), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without Desktop metadata, resolve cannot reattribute the reading.
	if _, err := a.ingestStatusLine(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := store.LatestSnapshots(ctx, at.Add(-time.Hour))
	if err != nil || len(stored) != 1 || stored[0].AccountRefHash != "wrong" {
		t.Fatalf("snapshot after unverified Desktop session = %+v, %v", stored, err)
	}
}

func TestIngestStatusLineDoesNotMoveAmbiguousReadings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, dir: dir, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	used, minutes := 42.0, 300
	snap := quota.Snapshot{Provider: account.ProviderAnthropic, AccountRefHash: "wrong", Source: quota.SourceClaudeStatusLine,
		ObservedAt: at, Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used, WindowMinutes: &minutes}}}
	if err := store.AddSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, ExternalRefHash: "correct", ObservedAt: at.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"s1", "s2"} {
		if _, err := store.IngestFile(ctx, session, scan.FileState{}, []usage.Event{{DedupeKey: session, Provider: account.ProviderAnthropic,
			AccountRefHash: "correct", Originator: "cli", SessionID: session, OccurredAt: at.Add(-time.Second)}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(a.SpoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"s1", "s2"} {
		spool := fmt.Sprintf(`{"session_id":%q,"observed_at":%q,"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":0}}}`, session, at.Format(time.RFC3339Nano))
		if err := os.WriteFile(filepath.Join(a.SpoolDir(), session+".json"), []byte(spool), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.ingestStatusLine(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := store.LatestSnapshots(ctx, at.Add(-time.Hour))
	if err != nil || len(stored) != 2 {
		t.Fatalf("ambiguous snapshots = %+v, %v; want old and new reading", stored, err)
	}
	for _, s := range stored {
		if s.AccountRefHash == "wrong" {
			return
		}
	}
	t.Fatal("ambiguous reading was moved")
}

func TestIngestFileCorrectsAttributionForTheOriginalSession(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	e := usage.Event{DedupeKey: "claude:m:r", Provider: account.ProviderAnthropic, Product: usage.ProductClaudeCode,
		Originator: "claude-desktop", SessionID: "desk", Model: "claude-opus-5-5", OccurredAt: t0.Add(time.Minute)}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	src := source{provider: account.ProviderAnthropic, parse: func(string) (parsed, error) { return parsed{events: []usage.Event{e}}, nil }}
	ac := accounts{provider: account.ProviderAnthropic, desktopSeen: true, desktop: map[string]string{"desk": "correct-org"}, since: t0}
	wrong := account.HashExternalRef(account.ProviderAnthropic, "wrong-org")
	correct := account.HashExternalRef(account.ProviderAnthropic, "correct-org")
	wrongEvent := e
	wrongEvent.AccountRefHash = wrong
	if _, err := store.IngestFile(ctx, path, scan.FileState{}, []usage.Event{wrongEvent}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteOutboxThrough(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n, err := a.ingestFile(ctx, src, path, scan.FileState{}, ac); err != nil || n != 1 {
		t.Fatalf("re-ingestion = %d, %v; want correction", n, err)
	}
	items, err := store.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	req, err := batchRequest(items)
	if err != nil || len(req.Events) != 1 || req.Events[0].AccountRefHash != correct || req.Events[0].PreviousAccountRefHash != wrong {
		t.Fatalf("corrected events = %+v, %v", req.Events, err)
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
		desktop:     map[string]string{"desk": "org-b", "listed": "org-c"},
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
		{"Cowork session", "desk", "local-agent", later, desk},
		// Desktop lists terminal sessions too, under whatever organization it
		// was on; they still ran under the CLI's sign-in.
		{"terminal session Desktop lists", "listed", "cli", later, "cli-max"},
		{"Desktop session resumed in the terminal", "desk", "cli", later, "cli-max"},
		{"Desktop session before joining", "desk", "claude-desktop", t0.Add(-time.Minute), ""},
		{"Desktop session without metadata", "gone", "claude-desktop", later, ""},
		{"Desktop with third-party inference", "desk", "claude-desktop-3p", later, ""},
		{"session with no recorded usage", "desk", "", later, "cli-max"},
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

// Requests from Claude clients to other vendors' models are recorded as
// third-party, against the account the client used. Claude Desktop set up
// for third-party inference has no Claude account, so its usage goes with
// the device's Claude Code account.
func TestIngestFileRecordsThirdPartyUsage(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	later := t0.Add(time.Hour)
	events := []usage.Event{
		{DedupeKey: "gateway", Provider: account.ProviderAnthropic, Originator: "cli", Model: "deepseek-v4-pro", ThirdParty: true, OccurredAt: later},
		{DedupeKey: "desktop-3p", Provider: account.ProviderAnthropic, Originator: "claude-desktop-3p", Model: "claude-sonnet-5", OccurredAt: later},
		{DedupeKey: "official", Provider: account.ProviderAnthropic, Originator: "cli", Model: "claude-opus-5-5", OccurredAt: later},
	}
	src := source{
		provider: account.ProviderAnthropic,
		parse:    func(string) (parsed, error) { return parsed{events: events}, nil },
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ac := accounts{
		provider: account.ProviderAnthropic,
		cli:      []account.Observation{{Provider: account.ProviderAnthropic, ExternalRefHash: "max", ObservedAt: t0}},
		since:    t0,
	}
	if n, err := a.ingestFile(ctx, src, path, scan.FileState{}, ac); err != nil || n != 3 {
		t.Fatalf("ingested %d, %v; want all 3", n, err)
	}
	items, err := store.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	req, err := batchRequest(items)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]syncapi.Event{}
	for _, e := range req.Events {
		got[e.DedupeKey] = e
	}
	for key, third := range map[string]bool{"gateway": true, "desktop-3p": true, "official": false} {
		if e := got[key]; e.AccountRefHash != "max" || e.ThirdParty != third {
			t.Errorf("%s = account %q, third party %v; want max, %v", key, e.AccountRefHash, e.ThirdParty, third)
		}
	}

	// A third-party request says nothing about the account Claude Desktop
	// is signed into.
	if ref, at, err := store.LastUse(ctx, account.ProviderAnthropic, []string{"claude-desktop-3p", "cli"}); err != nil || ref != "max" || !at.Equal(later) {
		t.Fatalf("LastUse = %q, %v, %v", ref, at, err)
	}
	third := []usage.Event{{DedupeKey: "desktop-gateway", Provider: account.ProviderAnthropic, Originator: "claude-desktop",
		Model: "glm-5.3", ThirdParty: true, OccurredAt: later.Add(time.Minute)}}
	src.parse = func(string) (parsed, error) { return parsed{events: third}, nil }
	desk := ac
	desk.desktopSeen, desk.desktop = true, map[string]string{"": "org"}
	if _, err := a.ingestFile(ctx, src, path, scan.FileState{Size: 1}, desk); err != nil {
		t.Fatal(err)
	}
	if _, at, err := store.LastUse(ctx, account.ProviderAnthropic, []string{"claude-desktop"}); err != nil || !at.IsZero() {
		t.Errorf("LastUse of Claude Desktop = %v, %v; want none from a third-party request", at, err)
	}
}

// A Claude client's request that drew on a ChatGPT subscription through a
// gateway goes with the Codex CLI's account at the time.
func TestIngestFileBooksOtherProvidersUsageToTheirAccount(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderOpenAI, ExternalRefHash: "plus", ObservedAt: t0}); err != nil {
		t.Fatal(err)
	}
	events := []usage.Event{
		{DedupeKey: "chatgpt", Provider: account.ProviderOpenAI, Originator: "claude-desktop", Model: "gpt-6-sol", Gateway: usage.GatewayOpenCodex, OccurredAt: t0.Add(time.Hour)},
		{DedupeKey: "before", Provider: account.ProviderOpenAI, Originator: "claude-desktop", Model: "gpt-6-sol", OccurredAt: t0.Add(-time.Hour)},
		{DedupeKey: "claude", Provider: account.ProviderAnthropic, Originator: "cli", Model: "claude-opus-5-5", OccurredAt: t0.Add(time.Hour)},
	}
	src := source{
		provider: account.ProviderAnthropic,
		parse:    func(string) (parsed, error) { return parsed{events: events}, nil },
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ac := accounts{
		provider: account.ProviderAnthropic,
		cli:      []account.Observation{{Provider: account.ProviderAnthropic, ExternalRefHash: "max", ObservedAt: t0}},
		since:    t0,
	}
	if _, err := a.ingestFile(ctx, src, path, scan.FileState{}, ac); err != nil {
		t.Fatal(err)
	}
	items, err := store.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	req, err := batchRequest(items)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]syncapi.Event{}
	for _, e := range req.Events {
		got[e.DedupeKey] = e
	}
	if e := got["chatgpt"]; e.Provider != "openai" || e.AccountRefHash != "plus" || e.ThirdParty {
		t.Errorf("chatgpt = %+v, want the Codex account's usage", e)
	}
	if _, ok := got["before"]; ok {
		t.Error("usage before the Codex CLI was first seen must not be uploaded")
	}
	if e := got["claude"]; e.AccountRefHash != "max" {
		t.Errorf("claude = %+v, want the Claude account", e)
	}
}
