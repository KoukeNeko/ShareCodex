package query_test

import (
	"context"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/server/query"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage/storagetest"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// A reading stored with a date after the server's clock, as the server once
// let a device with a fast clock do, must not stand over a real one.
func TestOverviewIgnoresReadingsDatedAfterNow(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	pc, mac := pair(t, store, "pc"), pair(t, store, "mac")
	now := time.Now().UTC().Truncate(time.Second)
	five := 300
	reading := func(used float64, resets, at time.Time) syncapi.SyncRequest {
		return syncapi.SyncRequest{Version: syncapi.Version, Snapshots: []syncapi.Snapshot{{
			Provider: "anthropic", AccountRefHash: "acct", Source: "claude-oauth-usage", ObservedAt: at,
			Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &five}}}}}
	}
	if _, err := store.Ingest(ctx, pc, reading(30, now.Add(-time.Hour), now.Add(15*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ingest(ctx, mac, reading(10, now.Add(2*time.Hour), now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}

	ov, err := query.Overview(ctx, store, "", now, now)
	if err != nil || len(ov.Accounts) != 1 || len(ov.Accounts[0].Buckets) != 1 {
		t.Fatalf("overview = %+v, %v", ov, err)
	}
	if five := ov.Accounts[0].Buckets[0]; five.Reset || five.UsedPercent != 10 {
		t.Errorf("5-hour window = %+v, want the mac's reading of 10%% over the dated-ahead one", five)
	}
}
