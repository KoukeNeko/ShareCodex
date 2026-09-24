package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/identity"
	"github.com/KoukeNeko/ShareCodex/internal/server/httpapi"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage/storagetest"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c client) do(method, path string, body, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			c.t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func pair(t *testing.T, store *storage.Store, base, person string) client {
	t.Helper()
	ctx := context.Background()
	p, err := store.AddPerson(ctx, person)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := store.CreateInvite(ctx, p.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c := client{t: t, base: base}
	var resp syncapi.PairResponse
	req := syncapi.PairRequest{Version: syncapi.Version, Code: code, DeviceName: person + "-laptop", Platform: "darwin"}
	if st := c.do("POST", syncapi.PathPair, req, &resp); st != http.StatusOK {
		t.Fatalf("pair status = %d", st)
	}
	if resp.PersonName != person || resp.Token == "" {
		t.Fatalf("pair response = %+v", resp)
	}
	if st := c.do("POST", syncapi.PathPair, req, nil); st != http.StatusForbidden {
		t.Fatalf("reusing an invite returned %d, want 403", st)
	}
	c.token = resp.Token
	return c
}

func ptr[T any](v T) *T { return &v }

func TestPairSyncOverview(t *testing.T) {
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	alice := pair(t, store, srv.URL, "alice")
	bob := pair(t, store, srv.URL, "bob")

	now := time.Now().UTC().Truncate(time.Second)
	resets := now.Add(2 * time.Hour)
	obs := syncapi.Observation{Provider: "openai", AccountRefHash: "acct-hash", Hint: "so***@example.com", PlanType: "plus", ObservedAt: now.Add(-time.Hour)}
	snap := syncapi.Snapshot{Provider: "openai", AccountRefHash: "acct-hash", Source: "codex-rollout", ObservedAt: now,
		Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: ptr(40.0), ResetsAt: &resets, WindowMinutes: ptr(300)}}}
	event := func(key string, output int64) syncapi.Event {
		return syncapi.Event{DedupeKey: key, AccountRefHash: "acct-hash", Provider: "openai", Product: "codex",
			Model: "gpt-5.5", OccurredAt: now.Add(-30 * time.Minute), Input: 1_000_000, Output: output}
	}

	aliceBatch := syncapi.SyncRequest{Version: syncapi.Version,
		Observations: []syncapi.Observation{obs}, Events: []syncapi.Event{event("codex:a1", 0), event("codex:a2", 0), event("codex:a3", 0)},
		Snapshots: []syncapi.Snapshot{snap}}
	var res syncapi.SyncResponse
	if st := alice.do("POST", syncapi.PathSync, aliceBatch, &res); st != http.StatusOK || res.Accepted != 5 {
		t.Fatalf("alice sync = %d, accepted %d; want 200, 5", st, res.Accepted)
	}
	if st := alice.do("POST", syncapi.PathSync, aliceBatch, &res); st != http.StatusOK || res.Accepted != 0 {
		t.Fatalf("re-sent batch accepted %d; want 0 (idempotent)", res.Accepted)
	}

	bobBatch := syncapi.SyncRequest{Version: syncapi.Version,
		Observations: []syncapi.Observation{obs}, Events: []syncapi.Event{event("codex:b1", 0)}}
	if st := bob.do("POST", syncapi.PathSync, bobBatch, &res); st != http.StatusOK {
		t.Fatalf("bob sync = %d", st)
	}

	var ov syncapi.Overview
	if st := bob.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK {
		t.Fatalf("overview status = %d", st)
	}
	if len(ov.Accounts) != 1 || len(ov.Accounts[0].Buckets) != 1 {
		t.Fatalf("overview = %+v", ov)
	}
	a := ov.Accounts[0]
	if a.Label != "so***@example.com" || a.PlanType != "plus" {
		t.Errorf("auto-created account = %+v", a)
	}
	b := a.Buckets[0]
	if b.UsedPercent != 40 || b.UnattributedPercent != 0 || len(b.Members) != 2 {
		t.Fatalf("bucket = %+v", b)
	}
	got := map[string]syncapi.MemberShare{}
	for _, m := range b.Members {
		got[m.Name] = m
	}
	// Equal weights split the allotment evenly; usage splits 3:1 by cost.
	if math.Abs(got["alice"].UsedPercent-30) > 1e-9 || math.Abs(got["bob"].UsedPercent-10) > 1e-9 {
		t.Errorf("used = alice %v, bob %v; want 30, 10", got["alice"].UsedPercent, got["bob"].UsedPercent)
	}
	if got["alice"].AllottedPercent != 50 || !got["bob"].IsYou || got["alice"].IsYou {
		t.Errorf("members = %+v", got)
	}
	if len(b.Models) != 1 || b.Models[0].Model != "gpt-5.5" || b.Models[0].Requests != 4 ||
		b.Models[0].Tokens != 4_000_000 || math.Abs(b.Models[0].UsedPercent-40) > 1e-9 {
		t.Errorf("models = %+v, want gpt-5.5 with all 4 requests and the whole 40%%", b.Models)
	}

	// A larger output for a known request replaces the partial one.
	if st := alice.do("POST", syncapi.PathSync, syncapi.SyncRequest{Version: syncapi.Version, Events: []syncapi.Event{event("codex:a1", 10)}}, &res); st != http.StatusOK || res.Accepted != 1 {
		t.Fatalf("larger output accepted %d; want 1", res.Accepted)
	}
}

func TestShareWeightsAndRevocation(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	alice := pair(t, store, srv.URL, "alice")
	pair(t, store, srv.URL, "bob")
	obs := syncapi.Observation{Provider: "anthropic", AccountRefHash: "max", Hint: "ma***", ObservedAt: time.Now().UTC()}
	if st := alice.do("POST", syncapi.PathSync, syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs}}, nil); st != http.StatusOK {
		t.Fatalf("sync = %d", st)
	}
	accounts, err := store.Accounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	acct := accounts[0]
	persons, _ := store.Persons(ctx)
	var bob identity.Person
	for _, p := range persons {
		if p.DisplayName == "bob" {
			bob = p
		}
	}
	if err := store.SetShareWeight(ctx, acct.ID, bob.ID, 3); err != nil {
		t.Fatal(err)
	}
	members, err := store.Members(ctx, acct.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v; want alice (auto) and bob (admin)", members, err)
	}

	if st := alice.do("POST", syncapi.PathSync, syncapi.SyncRequest{Version: 99}, nil); st != http.StatusUpgradeRequired {
		t.Errorf("old protocol version returned %d, want 426", st)
	}

	devices, _ := store.Devices(ctx)
	for _, d := range devices {
		if d.PersonName == "alice" {
			if err := store.RevokeDevice(ctx, d.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if st := alice.do("GET", syncapi.PathOverview, nil, nil); st != http.StatusUnauthorized {
		t.Errorf("revoked device returned %d, want 401", st)
	}
}
