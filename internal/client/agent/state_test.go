package agent

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
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

	if err := a.SetFineChart(true); err != nil {
		t.Fatal(err)
	}
	if !a.State(ctx).FineChart {
		t.Error("state does not show the full-detail chart after turning it on")
	}
	if saved, err := settings.Load(); err != nil || !saved.FineChart {
		t.Fatalf("saved fine chart = %v, %v; want it persisted", saved.FineChart, err)
	}

	if err := a.SetAccountSort("weekly"); err != nil || a.State(ctx).AccountSort != "weekly" {
		t.Fatalf("account sort = %q, %v; want weekly", a.State(ctx).AccountSort, err)
	}
	if err := a.SetAccountSort("name"); err == nil {
		t.Error("an unsupported account order must be rejected")
	}
	// Dragging lists the accounts in the dragged order.
	if err := a.SetAccountOrder([]string{"b", "a"}); err != nil {
		t.Fatal(err)
	}
	if saved, err := settings.Load(); err != nil || saved.AccountSort != "custom" || !slices.Equal(saved.AccountOrder, []string{"b", "a"}) {
		t.Fatalf("saved order = %q %v, %v; want custom b, a", saved.AccountSort, saved.AccountOrder, err)
	}

	// Pinned accounts reopen where they were moved to; pinning twice keeps
	// one widget.
	for _, id := range []string{"a", "b", "a"} {
		if err := a.PinAccount(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SetWidgetPosition("b", 40, 60); err != nil {
		t.Fatal(err)
	}
	if err := a.UnpinAccount("a"); err != nil {
		t.Fatal(err)
	}
	if got := a.State(ctx).Pinned; !slices.Equal(got, []string{"b"}) {
		t.Fatalf("pinned = %v, want b", got)
	}
	saved, err = settings.Load()
	if err != nil || len(saved.Widgets) != 1 || saved.Widgets[0] != (settings.Widget{AccountID: "b", Placed: true, X: 40, Y: 60}) {
		t.Fatalf("saved widgets = %+v, %v; want b placed at 40, 60", saved.Widgets, err)
	}
}

func TestLocalAccountExpiredReadingIsUnknown(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &Agent{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	now := time.Now()
	resets := now.Add(-time.Minute)
	used := 92.0
	if err := store.AddSnapshot(ctx, quota.Snapshot{Provider: account.ProviderAnthropic, AccountRefHash: "max",
		Source: quota.SourceClaudeStatusLine, ObservedAt: now.Add(-2 * time.Hour),
		Buckets: []quota.Bucket{{Key: quota.BucketFiveHour, UsedPercent: &used, ResetsAt: &resets}}}); err != nil {
		t.Fatal(err)
	}
	local := a.localAccounts(ctx, now)
	if len(local) != 1 || len(local[0].Buckets) != 1 || !local[0].Buckets[0].Reset {
		t.Fatalf("expired local quota = %+v", local)
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
	if first := a.localAccounts(ctx, now)[0]; first.Hint != "max-b" {
		t.Errorf("first account = %s, want max-b, the one signed in", first.Hint)
	}

	// Signing out leaves no account current.
	if err := store.AddObservation(ctx, account.Observation{Provider: account.ProviderAnthropic, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got := current(); got["max-a"] || got["max-b"] {
		t.Fatalf("current after sign-out = %v, want none", got)
	}
}
