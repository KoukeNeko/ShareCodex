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
	"slices"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
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
	p, err := store.AddPerson(context.Background(), person)
	if err != nil {
		t.Fatal(err)
	}
	return pairDevice(t, store, base, p, person+"-laptop", identity.PlatformDarwin)
}

// pairDevice joins another device for an existing person.
func pairDevice(t *testing.T, store *storage.Store, base string, p identity.Person, name string, platform identity.Platform) client {
	t.Helper()
	code, _, err := store.CreateInvite(context.Background(), p.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c := client{t: t, base: base}
	var resp syncapi.PairResponse
	req := syncapi.PairRequest{Version: syncapi.Version, Code: code, DeviceName: name, Platform: string(platform)}
	if st := c.do("POST", syncapi.PathPair, req, &resp); st != http.StatusOK {
		t.Fatalf("pair status = %d", st)
	}
	if resp.PersonID != p.ID || resp.PersonName != p.DisplayName || resp.Token == "" {
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

// One person's devices, such as a Mac and a Linux machine reached over SSH,
// add up to a single member.
func TestOnePersonManyDevices(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	alice, err := store.AddPerson(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	mac := pairDevice(t, store, srv.URL, alice, "mac", identity.PlatformDarwin)

	// The Mac creates the join link for the machine used over SSH.
	var inv syncapi.InviteResponse
	if st := mac.do("POST", syncapi.PathInvite, nil, &inv); st != http.StatusOK || inv.Code == "" {
		t.Fatalf("invite = %d, %+v", st, inv)
	}
	if time.Until(inv.ExpiresAt) > storage.InviteTTL || time.Until(inv.ExpiresAt) < storage.InviteTTL-time.Minute {
		t.Errorf("invite expires at %v, want about %v from now", inv.ExpiresAt, storage.InviteTTL)
	}
	server := client{t: t, base: srv.URL}
	var paired syncapi.PairResponse
	req := syncapi.PairRequest{Version: syncapi.Version, Code: inv.Code, DeviceName: "build-box", Platform: string(identity.PlatformLinux)}
	if st := server.do("POST", syncapi.PathPair, req, &paired); st != http.StatusOK || paired.PersonID != alice.ID {
		t.Fatalf("pair with a device's invite = %d, %+v; want alice", st, paired)
	}
	server.token = paired.Token
	if st := (client{t: t, base: srv.URL}).do("POST", syncapi.PathInvite, nil, nil); st != http.StatusUnauthorized {
		t.Errorf("invite without a device token returned %d, want 401", st)
	}

	now := time.Now().UTC().Truncate(time.Second)
	resets := now.Add(2 * time.Hour)
	obs := syncapi.Observation{Provider: "anthropic", AccountRefHash: "max", Hint: "ma***", PlanType: "max", ObservedAt: now.Add(-time.Hour)}
	event := func(key string) syncapi.Event {
		return syncapi.Event{DedupeKey: key, AccountRefHash: "max", Provider: "anthropic", Product: "claude-code",
			Model: "claude-opus-4-7", OccurredAt: now.Add(-30 * time.Minute), Input: 1_000_000}
	}
	snap := syncapi.Snapshot{Provider: "anthropic", AccountRefHash: "max", Source: "claude-statusline", ObservedAt: now,
		Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: ptr(30.0), ResetsAt: &resets, WindowMinutes: ptr(300)}}}

	for _, batch := range []struct {
		c   client
		req syncapi.SyncRequest
	}{
		{mac, syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs}, Events: []syncapi.Event{event("claude:m1:r1")}}},
		{server, syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs},
			Events: []syncapi.Event{event("claude:m2:r2"), event("claude:m3:r3")}, Snapshots: []syncapi.Snapshot{snap}}},
	} {
		if st := batch.c.do("POST", syncapi.PathSync, batch.req, nil); st != http.StatusOK {
			t.Fatalf("sync = %d", st)
		}
	}

	var ov syncapi.Overview
	if st := mac.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK {
		t.Fatalf("overview status = %d", st)
	}
	if len(ov.Accounts) != 1 || len(ov.Accounts[0].Buckets) != 1 {
		t.Fatalf("overview = %+v", ov)
	}
	b := ov.Accounts[0].Buckets[0]
	if len(b.Members) != 1 {
		t.Fatalf("members = %+v, want alice once", b.Members)
	}
	m := b.Members[0]
	if !m.IsYou || m.Requests != 3 || math.Abs(m.UsedPercent-30) > 1e-9 || b.UnattributedPercent != 0 {
		t.Errorf("member = %+v, unattributed %v; want both devices' 3 requests and the whole 30%%", m, b.UnattributedPercent)
	}

	devices, err := store.Devices(ctx)
	if err != nil || len(devices) != 2 {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
	for _, d := range devices {
		if d.PersonID != alice.ID {
			t.Errorf("device %s belongs to %s, want alice", d.Name, d.PersonName)
		}
	}
}

// The overview shows who is signed into each account right now: each
// device's latest account per provider, while its last report is recent.
func TestActiveUsers(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	alice, err := store.AddPerson(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	mac := pairDevice(t, store, srv.URL, alice, "mac", identity.PlatformDarwin)
	box := pairDevice(t, store, srv.URL, alice, "build-box", identity.PlatformLinux)
	bob := pair(t, store, srv.URL, "bob")
	carol := pair(t, store, srv.URL, "carol")
	dave := pair(t, store, srv.URL, "dave")

	now := time.Now().UTC().Truncate(time.Second)
	obs := func(ref string, age time.Duration) syncapi.Observation {
		return syncapi.Observation{Provider: "anthropic", AccountRefHash: ref, Hint: ref, ObservedAt: now.Add(-age)}
	}
	codex := syncapi.Observation{Provider: "openai", AccountRefHash: "plus", Hint: "plus", ObservedAt: now}
	desktop := func(ref string) syncapi.Observation {
		o := obs(ref, 3*time.Minute)
		o.Source, o.Hint = string(account.SourceClaudeDesktop), ""
		return o
	}
	for _, batch := range []struct {
		c   client
		obs []syncapi.Observation
	}{
		// Alice's Mac runs the CLI on max-a and Claude Desktop on max-b.
		{mac, []syncapi.Observation{obs("max-a", time.Minute), codex, desktop("max-b")}},
		// Her build box runs both on max-a; it is listed once.
		{box, []syncapi.Observation{obs("max-a", 2*time.Minute), desktop("max-a")}},
		// Bob switched from max-a to max-b.
		{bob, []syncapi.Observation{obs("max-a", 10*time.Minute), obs("max-b", time.Minute)}},
		// Carol's last report is too old to count as signed in now.
		{carol, []syncapi.Observation{obs("max-a", time.Hour)}},
		{dave, []syncapi.Observation{obs("max-b", time.Minute)}},
	} {
		req := syncapi.SyncRequest{Version: syncapi.Version, Observations: batch.obs}
		if st := batch.c.do("POST", syncapi.PathSync, req, nil); st != http.StatusOK {
			t.Fatalf("sync = %d", st)
		}
	}
	devices, err := store.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.PersonName == "dave" {
			if err := store.RevokeDevice(ctx, d.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	var ov syncapi.Overview
	if st := bob.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK {
		t.Fatalf("overview status = %d", st)
	}
	got := map[string][]syncapi.ActiveUser{}
	for _, a := range ov.Accounts {
		got[a.Label] = a.ActiveUsers
	}
	want := map[string][]syncapi.ActiveUser{
		"max-a": {{PersonID: alice.ID, Name: "alice", Devices: []string{"build-box", "mac"}}},
		"max-b": {{Name: "bob", IsYou: true, Devices: []string{"bob-laptop"}}, {PersonID: alice.ID, Name: "alice", Devices: []string{"mac"}}},
		"plus":  {{PersonID: alice.ID, Name: "alice", Devices: []string{"mac"}}},
	}
	for label, users := range want {
		if len(got[label]) != len(users) {
			t.Errorf("%s: active users = %+v, want %+v", label, got[label], users)
			continue
		}
		for i, u := range users {
			g := got[label][i]
			if g.Name != u.Name || g.IsYou != u.IsYou || !slices.Equal(g.Devices, u.Devices) || (u.PersonID != "" && g.PersonID != u.PersonID) {
				t.Errorf("%s: active user %d = %+v, want %+v", label, i, g, u)
			}
		}
	}
}
