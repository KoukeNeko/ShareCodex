package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
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

func TestCorrectSnapshotMovesRecordedReadingOnce(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.Unix(100, 0)
	used := 42.0
	snap := quota.Snapshot{Provider: account.ProviderAnthropic, AccountRefHash: "wrong", Source: quota.SourceClaudeStatusLine,
		ObservedAt: at, Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used}}}
	if err := s.AddSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOutboxThrough(ctx, 1); err != nil {
		t.Fatal(err)
	}
	snap.AccountRefHash = "correct"
	corrected, err := s.CorrectSnapshot(ctx, snap)
	if err != nil || !corrected {
		t.Fatalf("correction = %v, %v", corrected, err)
	}
	corrected, err = s.CorrectSnapshot(ctx, snap)
	if err != nil || corrected {
		t.Fatalf("replayed correction = %v, %v; want no change", corrected, err)
	}
	stored, err := s.LatestSnapshots(ctx, time.Time{})
	if err != nil || len(stored) != 1 || stored[0].AccountRefHash != "correct" {
		t.Fatalf("snapshots = %+v, %v", stored, err)
	}
	items, err := s.PendingOutbox(ctx, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("correction outbox = %+v, %v", items, err)
	}
	var correctedSnapshot syncapi.Snapshot
	if err := json.Unmarshal(items[0].Payload, &correctedSnapshot); err != nil || correctedSnapshot.PreviousAccountRefHash != "wrong" {
		t.Errorf("correction payload = %+v, %v", correctedSnapshot, err)
	}
}

func TestCorrectSnapshotKeepsAmbiguousReadings(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	used := 42.0
	snap := quota.Snapshot{Provider: account.ProviderAnthropic, Source: quota.SourceClaudeStatusLine,
		ObservedAt: time.Unix(100, 0), Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used}}}
	for _, ref := range []string{"wrong", "correct"} {
		snap.AccountRefHash = ref
		if err := s.AddSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
	}
	if corrected, err := s.CorrectSnapshot(ctx, snap); err != nil || corrected {
		t.Fatalf("ambiguous correction = %v, %v; want no change", corrected, err)
	}
	stored, err := s.LatestSnapshots(ctx, time.Time{})
	if err != nil || len(stored) != 2 {
		t.Fatalf("snapshots = %+v, %v; want both readings", stored, err)
	}
}

func TestReattributeRecordedUsage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	st := scan.FileState{Size: 10, ModTime: time.Unix(100, 0)}
	e := usage.Event{DedupeKey: "claude:m1:r1", AccountRefHash: "wrong", Provider: account.ProviderAnthropic,
		Product: usage.ProductClaudeCode, Originator: "claude-desktop", SessionID: "desk", Model: "claude-opus-5-5",
		OccurredAt: time.Unix(50, 0), Tokens: usage.Tokens{Input: 10, Output: 12}}
	if _, err := s.IngestFile(ctx, "f.jsonl", st, []usage.Event{e}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOutboxThrough(ctx, 1); err != nil {
		t.Fatal(err)
	}
	e.AccountRefHash = "correct"
	if n, err := s.IngestFile(ctx, "f.jsonl", st, []usage.Event{e}, nil); err != nil || n != 1 {
		t.Fatalf("corrected event = %d, %v; want one update", n, err)
	}
	items, err := s.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var corrected syncapi.Event
	if len(items) != 1 || json.Unmarshal(items[0].Payload, &corrected) != nil || corrected.AccountRefHash != "correct" || corrected.PreviousAccountRefHash != "wrong" {
		t.Fatalf("correction outbox = %+v, decoded %+v", items, corrected)
	}
}

func TestResetLedgerKeepsSignInTimeline(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	st := scan.FileState{Size: 10, ModTime: time.Unix(100, 0)}
	at := time.Unix(50, 0)
	e := usage.Event{DedupeKey: "claude:m1:r1", AccountRefHash: "acct", Provider: account.ProviderAnthropic,
		Product: usage.ProductClaudeCode, Model: "claude-opus-5-5", OccurredAt: at, Tokens: usage.Tokens{Input: 10, Output: 12}}
	if _, err := s.IngestFile(ctx, "f.jsonl", st, []usage.Event{e}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, ExternalRefHash: "acct", ObservedAt: at}); err != nil {
		t.Fatal(err)
	}
	used, minutes := 42.0, 300
	if err := s.AddSnapshot(ctx, quota.Snapshot{Provider: account.ProviderAnthropic, AccountRefHash: "acct", Source: quota.SourceClaudeStatusLine,
		ObservedAt: at, Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used, WindowMinutes: &minutes}}}); err != nil {
		t.Fatal(err)
	}

	if err := s.ResetLedger(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.EventCount(ctx); err != nil || n != 0 {
		t.Errorf("events after reset = %d, %v; want none", n, err)
	}
	if files, err := s.KnownFiles(ctx); err != nil || len(files) != 0 {
		t.Errorf("files after reset = %v, %v; want none, so every session is read again", files, err)
	}
	if n, err := s.OutboxLen(ctx); err != nil || n != 0 {
		t.Errorf("outbox after reset = %d, %v; want none", n, err)
	}
	obs, err := s.Observations(ctx, account.ProviderAnthropic, account.SourceCLI)
	if err != nil || len(obs) != 1 || !obs[0].ObservedAt.Equal(at) {
		t.Errorf("observations after reset = %+v, %v; want the sign-in timeline kept", obs, err)
	}
	if snaps, err := s.LatestSnapshots(ctx, at.Add(-time.Minute)); err != nil || len(snaps) != 1 {
		t.Errorf("snapshots after reset = %+v, %v; want them kept", snaps, err)
	}
}

func TestSessionOriginatorAndLastUse(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	event := func(key, ref, session, originator string, at int64) usage.Event {
		return usage.Event{DedupeKey: key, AccountRefHash: ref, Provider: account.ProviderAnthropic,
			Product: usage.ProductClaudeCode, Originator: originator, SessionID: session, Model: "claude-opus-5-5",
			OccurredAt: time.Unix(at, 0)}
	}
	events := []usage.Event{
		event("e1", "org-a", "desk", "claude-desktop", 100),
		event("e2", "cli-max", "term", "cli", 300),
		// The Desktop session was resumed in the terminal later.
		event("e3", "cli-max", "desk", "cli", 400),
		event("e4", "org-b", "cowork", "local-agent", 200),
	}
	if _, err := s.IngestFile(ctx, "f.jsonl", scan.FileState{}, events, nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		session string
		at      int64
		want    string
	}{
		{"desk", 150, "claude-desktop"},
		{"desk", 450, "cli"},
		{"term", 350, "cli"},
		{"cowork", 250, "local-agent"},
		{"desk", 50, ""},
		{"none", 450, ""},
	} {
		if got, err := s.SessionOriginatorAt(ctx, tc.session, time.Unix(tc.at, 0)); err != nil || got != tc.want {
			t.Errorf("SessionOriginatorAt(%s, %d) = %q, %v; want %q", tc.session, tc.at, got, err, tc.want)
		}
	}

	ref, at, err := s.LastUse(ctx, account.ProviderAnthropic, []string{"claude-desktop", "local-agent"})
	if err != nil || ref != "org-b" || !at.Equal(time.Unix(200, 0)) {
		t.Errorf("LastUse = %q at %v, %v; want org-b at 200s", ref, at, err)
	}
	if ref, at, err := s.LastUse(ctx, account.ProviderOpenAI, []string{"claude-desktop"}); err != nil || ref != "" || !at.IsZero() {
		t.Errorf("LastUse without events = %q at %v, %v; want none", ref, at, err)
	}
}
