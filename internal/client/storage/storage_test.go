package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/scan"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIngestFileIsIdempotentAndKeepsLargerOutput(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	st := scan.FileState{Size: 10, ModTime: time.Unix(100, 0)}
	e := usage.Event{
		DedupeKey: "claude:m1:r1", AccountRefHash: "acct", Provider: account.ProviderAnthropic,
		Product: usage.ProductClaudeCode, Model: "claude-opus-5-5", OccurredAt: time.Unix(50, 0),
		Tokens: usage.Tokens{Input: 10, Output: 12},
	}

	if n, err := s.IngestFile(ctx, "f.jsonl", st, []usage.Event{e}, nil); err != nil || n != 1 {
		t.Fatalf("first ingest = %d, %v; want 1", n, err)
	}
	if n, _ := s.IngestFile(ctx, "f.jsonl", st, []usage.Event{e}, nil); n != 0 {
		t.Fatalf("re-ingesting the same event changed %d rows; want 0", n)
	}
	e.Tokens.Output = 200
	if n, _ := s.IngestFile(ctx, "f.jsonl", st, []usage.Event{e}, nil); n != 1 {
		t.Fatalf("larger output should update the event, changed %d", n)
	}
	e.Tokens.Output = 50
	if n, _ := s.IngestFile(ctx, "f.jsonl", st, []usage.Event{e}, nil); n != 0 {
		t.Fatalf("smaller output must not overwrite, changed %d", n)
	}

	items, err := s.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("outbox has %d items, want 2 (insert + larger update)", len(items))
	}
	if err := s.DeleteOutboxThrough(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.OutboxLen(ctx); n != 1 {
		t.Fatalf("outbox len = %d after partial ack, want 1", n)
	}

	known, err := s.KnownFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := known["f.jsonl"]; got.Size != 10 || !got.ModTime.Equal(st.ModTime) {
		t.Errorf("file state = %+v, want %+v", got, st)
	}
}

func TestObservationsWithoutPooledAccountStayLocal(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.UnixMilli(1_000)

	if err := s.AddObservation(ctx, account.Observation{Provider: account.ProviderOpenAI, ExternalRefHash: "", ObservedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddObservation(ctx, account.Observation{Provider: account.ProviderOpenAI, ExternalRefHash: "h", ObservedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	obs, err := s.Observations(ctx, account.ProviderOpenAI, account.SourceCLI)
	if err != nil || len(obs) != 2 {
		t.Fatalf("observations = %v, %v; want 2", obs, err)
	}
	if n, _ := s.OutboxLen(ctx); n != 1 {
		t.Fatalf("outbox len = %d, want 1 (API-key period is not synced)", n)
	}
}

func TestObservationTimelinesAreKeptPerApp(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.UnixMilli(1_000)

	for _, o := range []account.Observation{
		{Provider: account.ProviderAnthropic, ExternalRefHash: "cli", ObservedAt: at},
		// Claude Desktop on another account at the same moment.
		{Provider: account.ProviderAnthropic, Source: account.SourceClaudeDesktop, ExternalRefHash: "desktop", ObservedAt: at},
	} {
		if err := s.AddObservation(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	cli, err := s.Observations(ctx, account.ProviderAnthropic, account.SourceCLI)
	if err != nil || len(cli) != 1 || cli[0].ExternalRefHash != "cli" {
		t.Fatalf("CLI timeline = %+v, %v; want only the CLI's account", cli, err)
	}
	dsk, err := s.Observations(ctx, account.ProviderAnthropic, account.SourceClaudeDesktop)
	if err != nil || len(dsk) != 1 || dsk[0].ExternalRefHash != "desktop" {
		t.Fatalf("Desktop timeline = %+v, %v; want only Desktop's account", dsk, err)
	}

	items, err := s.PendingOutbox(ctx, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("outbox = %d items, %v; want both observations", len(items), err)
	}
	var o syncapi.Observation
	if err := json.Unmarshal(items[1].Payload, &o); err != nil || o.Source != string(account.SourceClaudeDesktop) {
		t.Errorf("queued Desktop observation = %+v, %v; want its source sent", o, err)
	}
}
