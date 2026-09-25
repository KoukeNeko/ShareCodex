package agent

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/client/storage"
	"github.com/KoukeNeko/ShareCodex/internal/client/update"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

func TestStateCarriesLanguageAndUpdate(t *testing.T) {
	t.Setenv("SHARECODEX_HOME", t.TempDir())
	ctx := context.Background()
	a, err := New(ctx, "0.1.0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	st := a.State(ctx)
	if st.Language != "en" || st.Update != nil {
		t.Fatalf("fresh state = language %q, update %v; want English and no update", st.Language, st.Update)
	}

	a.update = &update.Release{Version: "0.2.0", URL: "https://example.com"}
	if err := a.SetLanguage("zh-TW"); err != nil {
		t.Fatal(err)
	}
	st = a.State(ctx)
	if st.Language != "zh-TW" || st.Update == nil || st.Update.Version != "0.2.0" {
		t.Fatalf("state = language %q, update %+v", st.Language, st.Update)
	}
	saved, err := settings.Load()
	if err != nil || saved.Language != "zh-TW" {
		t.Fatalf("saved language = %q, %v; want it persisted", saved.Language, err)
	}

	if err := a.SetLanguage("fr"); err == nil {
		t.Fatal("an unsupported language must be rejected")
	}
}

func TestLocalAccountsMarkTheSignedInAccount(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	now := time.Now()
	used := 10.0
	for i, ref := range []string{"max-a", "max-b"} {
		at := now.Add(time.Duration(i-3) * time.Minute)
		if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, ExternalRefHash: ref, Hint: ref, ObservedAt: at}); err != nil {
			t.Fatal(err)
		}
		snap := quota.Snapshot{Provider: account.ProviderAnthropic, AccountRefHash: ref, Source: quota.SourceClaudeStatusLine, ObservedAt: at,
			Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used}}}
		if err := store.AddSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
	}

	current := func() map[string]bool {
		out := map[string]bool{}
		for _, la := range a.localAccounts(ctx, now) {
			out[la.Hint] = la.Current
		}
		return out
	}
	if got := current(); len(got) != 2 || got["max-a"] || !got["max-b"] {
		t.Fatalf("current = %v, want only max-b, the latest sign-in", got)
	}

	// Signing out leaves no account current.
	if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got := current(); got["max-a"] || got["max-b"] {
		t.Fatalf("current after sign-out = %v, want none", got)
	}
}
