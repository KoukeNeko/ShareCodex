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

	n, err := a.ingestFile(ctx, src, path, scan.FileState{Size: 0, ModTime: t0}, obs)
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
