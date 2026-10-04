package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/server/storage/storagetest"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// The report is shared through the cache, so redacting it must leave the
// cached copy as it was.
func TestPublicCapacityLeavesTheCachedReportAlone(t *testing.T) {
	cached := syncapi.CapacityReport{
		AccountLabel: "al***@example.com",
		Plans:        []syncapi.PlanIntervalDTO{{PlanType: "max 5x", Reason: "private", Source: "admin"}},
	}
	got := publicCapacity(cached, "Account 1")
	if got.AccountLabel != "Account 1" || got.Plans[0].Reason != "" || got.Plans[0].Source != "" {
		t.Errorf("redacted report = %+v", got)
	}
	if cached.AccountLabel != "al***@example.com" || cached.Plans[0].Reason != "private" || cached.Plans[0].Source != "admin" {
		t.Errorf("cached report changed: %+v", cached)
	}
}

// Anyone can vary a custom range without end, so only the presets are
// cached.
func TestDashboardCapacityCachesOnlyPresets(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	person, err := store.AddPerson(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := store.CreateInvite(ctx, person.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := store.Pair(ctx, code, "alice-laptop", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obs := syncapi.Observation{Provider: "anthropic", AccountRefHash: "acct", Hint: "al***@example.com", ObservedAt: now}
	if _, err := store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPublicDashboard(ctx, true); err != nil {
		t.Fatal(err)
	}
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	c := &Console{store: store, log: slog.New(slog.NewTextHandler(io.Discard, nil)), pages: pages}
	get := func(q url.Values) {
		t.Helper()
		rec := httptest.NewRecorder()
		c.dashboardCapacity(rec, httptest.NewRequest(http.MethodGet, dashboardCapacityPath+"?"+q.Encode(), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%v returned %d", q, rec.Code)
		}
	}

	get(url.Values{"period": {"7d"}})
	if n := len(c.dash.capacity.entries); n != 1 {
		t.Fatalf("a preset left %d cached reports, want 1", n)
	}
	for i := range 3 {
		get(url.Values{"from": {now.Add(-time.Duration(48+i) * time.Hour).Format(time.RFC3339)}, "to": {now.Add(-24 * time.Hour).Format(time.RFC3339)}})
	}
	if n := len(c.dash.capacity.entries); n != 1 {
		t.Errorf("custom ranges left %d cached reports, want the preset's 1", n)
	}
}
