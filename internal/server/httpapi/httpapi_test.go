package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// An account read through a ShareCodex sign-in has no observation to name it.
// Its snapshot names it instead, without marking anyone as signed into it.
func TestSnapshotNamesAccountKnownOnlyFromSignIn(t *testing.T) {
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	alice := pair(t, store, srv.URL, "alice")

	now := time.Now().UTC().Truncate(time.Second)
	snap := syncapi.Snapshot{Provider: "anthropic", AccountRefHash: "desktop-only", AccountHint: "de***@example.com",
		PlanType: "max", Source: "claude-oauth-usage", ObservedAt: now,
		Buckets: []syncapi.Bucket{{Key: "weekly", UsedPercent: ptr(31.0), WindowMinutes: ptr(10080)}}}
	var res syncapi.SyncResponse
	if st := alice.do("POST", syncapi.PathSync, syncapi.SyncRequest{Version: syncapi.Version, Snapshots: []syncapi.Snapshot{snap}}, &res); st != http.StatusOK || res.Accepted != 1 {
		t.Fatalf("sync = %d, accepted %d; want 200, 1", st, res.Accepted)
	}
	var ov syncapi.Overview
	if st := alice.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK || len(ov.Accounts) != 1 {
		t.Fatalf("overview = %d, %+v", st, ov)
	}
	a := ov.Accounts[0]
	if a.Label != "de***@example.com" || a.PlanType != "max" || len(a.Buckets) != 1 {
		t.Errorf("account = %+v, want it named by the snapshot", a)
	}
	if len(a.ActiveUsers) != 0 {
		t.Errorf("active users = %+v; a sign-in is not a sign-in on the CLI", a.ActiveUsers)
	}
}

// The dedupe key is global, so whichever device uploads a request first fixes
// its account. A device that parsed the same request differently must still be
// able to correct the account without becoming the record's owner.
func TestSyncCorrectsAccountAttributionWithoutChangingOwner(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	alicePerson, err := store.AddPerson(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	alice := pairDevice(t, store, srv.URL, alicePerson, "alice-laptop", identity.PlatformDarwin)
	bob := pair(t, store, srv.URL, "bob")
	carol := pair(t, store, srv.URL, "carol")
	now := time.Now().UTC().Truncate(time.Second)
	resets := now.Add(time.Hour)
	event := syncapi.Event{DedupeKey: "claude:m1:r1", AccountRefHash: "wrong", Provider: "anthropic",
		Product: "claude-code", Originator: "claude-desktop", SessionID: "desk", Model: "claude-opus-5-5", OccurredAt: now, Input: 100}
	snap := syncapi.Snapshot{Provider: "anthropic", AccountRefHash: "wrong", Source: "claude-statusline", ObservedAt: now,
		Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: ptr(30.0), ResetsAt: &resets, WindowMinutes: ptr(300)}}}
	sync := func(c client, req syncapi.SyncRequest) (int, syncapi.SyncResponse) {
		var res syncapi.SyncResponse
		st := c.do("POST", syncapi.PathSync, req, &res)
		return st, res
	}
	if st, _ := sync(alice, syncapi.SyncRequest{Version: syncapi.Version, Events: []syncapi.Event{event}, Snapshots: []syncapi.Snapshot{snap}}); st != http.StatusOK {
		t.Fatalf("initial sync = %d", st)
	}
	event.AccountRefHash, snap.AccountRefHash = "correct", "correct"
	event.PreviousAccountRefHash, snap.PreviousAccountRefHash = "wrong", "wrong"

	// Bob never stored this request, but his previous ref names the account
	// the server actually holds, so the correction applies.
	if st, res := sync(bob, syncapi.SyncRequest{Version: syncapi.Version, Events: []syncapi.Event{event}}); st != http.StatusOK || res.Accepted != 1 || res.Diverged != 0 {
		t.Fatalf("cross-device correction = %d, accepted %d, diverged %d; want 200, 1, 0", st, res.Accepted, res.Diverged)
	}
	if st, res := sync(bob, syncapi.SyncRequest{Version: syncapi.Version, Events: []syncapi.Event{event}}); st != http.StatusOK || res.Accepted != 0 || res.Diverged != 0 {
		t.Fatalf("replayed correction = %d, accepted %d, diverged %d; want 200, 0, 0", st, res.Accepted, res.Diverged)
	}

	// Carol sends no previous ref, so nothing ties her view to the stored row;
	// the server keeps the row and tells her the two views diverged.
	third := event
	third.AccountRefHash, third.PreviousAccountRefHash = "third", ""
	if st, res := sync(carol, syncapi.SyncRequest{Version: syncapi.Version, Events: []syncapi.Event{third}}); st != http.StatusOK || res.Accepted != 0 || res.Diverged != 1 {
		t.Fatalf("unfounded correction = %d, accepted %d, diverged %d; want 200, 0, 1 (diverged)", st, res.Accepted, res.Diverged)
	}

	// Snapshot correction stays with the device that took the reading.
	if st, res := sync(alice, syncapi.SyncRequest{Version: syncapi.Version, Snapshots: []syncapi.Snapshot{snap}}); st != http.StatusOK || res.Accepted != 2 {
		t.Fatalf("snapshot correction = %d, accepted %d; want 200, 2 (delete, insert)", st, res.Accepted)
	}
	if st, res := sync(alice, syncapi.SyncRequest{Version: syncapi.Version, Snapshots: []syncapi.Snapshot{snap}}); st != http.StatusOK || res.Accepted != 0 {
		t.Fatalf("replayed snapshot correction accepted %d, want 0", res.Accepted)
	}

	accounts, err := store.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		rows, err := store.Usage(ctx, a.ID, now.Add(-time.Minute), now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if a.ExternalRefHash == "wrong" && len(rows) != 0 || a.ExternalRefHash == "correct" && (len(rows) != 1 || rows[0].Tokens.Input != 100) {
			t.Errorf("account %s usage = %+v", a.ExternalRefHash, rows)
		}
		if a.ExternalRefHash == "correct" && rows[0].PersonID != alicePerson.ID {
			t.Errorf("correction changed the owner: person %s, want %s", rows[0].PersonID, alicePerson.ID)
		}
		// "third" exists only in carol's view; the divergence count above is
		// the whole record of it.
		if a.ExternalRefHash == "third" && len(rows) != 0 {
			t.Errorf("rejected account holds usage = %+v", rows)
		}
		snaps, err := store.Snapshots(ctx, a.ID, now.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if a.ExternalRefHash == "wrong" && len(snaps) != 0 || a.ExternalRefHash == "correct" && len(snaps) != 1 {
			t.Errorf("account %s snapshots = %+v", a.ExternalRefHash, snaps)
		}
	}
}

func TestSyncAcceptsExistingProtocolDuringUpgrade(t *testing.T) {
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	alice := pair(t, store, srv.URL, "alice")
	now := time.Now().UTC().Truncate(time.Second)
	var res syncapi.SyncResponse
	initial := syncapi.SyncRequest{Version: 1, Events: []syncapi.Event{{DedupeKey: "legacy-event", AccountRefHash: "wrong", Provider: "anthropic", SessionID: "desk", Originator: "cli", OccurredAt: now}}}
	if st := alice.do("POST", syncapi.PathSync, initial, &res); st != http.StatusOK || res.Accepted != 1 {
		t.Fatalf("version 1 sync = %d, accepted %d", st, res.Accepted)
	}
	initial.Events[0].AccountRefHash = "correct"
	initial.Events[0].PreviousAccountRefHash = "wrong"
	if st := alice.do("POST", syncapi.PathSync, initial, &res); st != http.StatusOK || res.Accepted != 0 {
		t.Fatalf("version 1 forged correction = %d, accepted %d", st, res.Accepted)
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
	var order []string
	for _, a := range ov.Accounts {
		got[a.Label] = a.ActiveUsers
		order = append(order, a.Label)
	}
	// Bob's own account comes first, then the rest in their usual order.
	if want := []string{"max-b", "max-a", "plus"}; !slices.Equal(order, want) {
		t.Errorf("account order = %v, want %v", order, want)
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

// A member who leaves an account drops out of its allotment and no longer
// sees it; signing in to it again does not add them back.
func TestLeaveAccount(t *testing.T) {
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	alice := pair(t, store, srv.URL, "alice")
	bob := pair(t, store, srv.URL, "bob")
	now := time.Now().UTC().Truncate(time.Second)
	resets := now.Add(time.Hour)
	sync := syncapi.SyncRequest{Version: syncapi.Version,
		Observations: []syncapi.Observation{{Provider: "anthropic", AccountRefHash: "max", Hint: "ma***", ObservedAt: now}},
		Snapshots: []syncapi.Snapshot{{Provider: "anthropic", AccountRefHash: "max", Source: "claude-statusline", ObservedAt: now,
			Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: ptr(10.0), ResetsAt: &resets, WindowMinutes: ptr(300)}}}}}
	for _, c := range []client{alice, bob} {
		if st := c.do("POST", syncapi.PathSync, sync, nil); st != http.StatusOK {
			t.Fatalf("sync = %d", st)
		}
	}
	var ov syncapi.Overview
	if st := bob.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK || len(ov.Accounts) != 1 {
		t.Fatalf("overview = %d, %+v", st, ov)
	}
	id := ov.Accounts[0].ID

	if st := bob.do("POST", syncapi.PathAccounts+"missing/leave", nil, nil); st != http.StatusNotFound {
		t.Errorf("leaving an unknown account = %d, want 404", st)
	}
	if st := bob.do("POST", syncapi.PathAccounts+id+"/leave", nil, nil); st != http.StatusOK {
		t.Fatalf("leave = %d", st)
	}
	if st := bob.do("POST", syncapi.PathSync, sync, nil); st != http.StatusOK {
		t.Fatalf("sync = %d", st)
	}

	ov = syncapi.Overview{}
	if st := bob.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK || len(ov.Accounts) != 0 {
		t.Errorf("bob's overview after leaving = %d, %+v; want no accounts", st, ov)
	}
	ov = syncapi.Overview{}
	if st := alice.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK || len(ov.Accounts) != 1 {
		t.Fatalf("alice's overview = %d, %+v", st, ov)
	}
	for _, m := range ov.Accounts[0].Buckets[0].Members {
		if m.Name == "alice" && m.AllottedPercent != 100 {
			t.Errorf("alice allotted %v, want 100 after bob left", m.AllottedPercent)
		}
		if m.Name == "bob" && m.AllottedPercent != 0 {
			t.Errorf("bob allotted %v, want 0 after leaving", m.AllottedPercent)
		}
	}
}

// Claude's plan comes from usage readings, which ask Anthropic: a newer one
// changes it, an older one replayed by a resync does not, and a plain "max"
// keeps the tier. claude auth status reports the plan stored with the
// sign-in, which can be from before an upgrade, so observations never set it.
func TestClaudePlanFollowsNewestUsageReading(t *testing.T) {
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	alice := pair(t, store, srv.URL, "alice")

	now := time.Now().UTC().Truncate(time.Second)
	observe := func(plan string, at time.Time) syncapi.Observation {
		return syncapi.Observation{Provider: "anthropic", AccountRefHash: "max", Hint: "ma***", PlanType: plan, ObservedAt: at}
	}
	reading := func(plan string, at time.Time) syncapi.Snapshot {
		return syncapi.Snapshot{Provider: "anthropic", AccountRefHash: "max", PlanType: plan, Source: "claude-oauth-usage",
			ObservedAt: at, Buckets: []syncapi.Bucket{{Key: "weekly", UsedPercent: ptr(5.0), WindowMinutes: ptr(10080)}}}
	}
	plan := func(req syncapi.SyncRequest) string {
		t.Helper()
		req.Version = syncapi.Version
		if st := alice.do("POST", syncapi.PathSync, req, nil); st != http.StatusOK {
			t.Fatalf("sync = %d", st)
		}
		var ov syncapi.Overview
		if st := alice.do("GET", syncapi.PathOverview, nil, &ov); st != http.StatusOK || len(ov.Accounts) != 1 {
			t.Fatalf("overview = %d, %+v", st, ov)
		}
		return ov.Accounts[0].PlanType
	}

	if got := plan(syncapi.SyncRequest{Observations: []syncapi.Observation{observe("pro", now)}}); got != "" {
		t.Errorf("plan from a sign-in observation = %q, want none", got)
	}
	if got := plan(syncapi.SyncRequest{Snapshots: []syncapi.Snapshot{reading("max 20x", now)}}); got != "max 20x" {
		t.Errorf("plan after a usage reading = %q, want max 20x", got)
	}
	if got := plan(syncapi.SyncRequest{Observations: []syncapi.Observation{observe("pro", now.Add(time.Minute))}}); got != "max 20x" {
		t.Errorf("plan after a newer stale sign-in = %q, want max 20x", got)
	}
	if got := plan(syncapi.SyncRequest{Snapshots: []syncapi.Snapshot{reading("max", now.Add(time.Minute))}}); got != "max 20x" {
		t.Errorf("plan after a plain max reading = %q, want max 20x", got)
	}
	if got := plan(syncapi.SyncRequest{Snapshots: []syncapi.Snapshot{reading("pro", now.Add(-2*time.Hour))}}); got != "max 20x" {
		t.Errorf("plan after replaying an older pro reading = %q, want max 20x", got)
	}
	if got := plan(syncapi.SyncRequest{Snapshots: []syncapi.Snapshot{reading("pro", now.Add(2*time.Minute))}}); got != "pro" {
		t.Errorf("plan after a downgrade = %q, want pro", got)
	}
}

// The overview carries the viewer's own totals since the start of their day
// and over 30 days, across accounts, and each window's tokens over time.
func TestPersonalUsageAndTimeline(t *testing.T) {
	store := storagetest.New(t)
	srv := httptest.NewServer(httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	alice := pair(t, store, srv.URL, "alice")
	bob := pair(t, store, srv.URL, "bob")

	now := time.Now().UTC().Truncate(time.Second)
	resets := now.Add(time.Hour)
	start := resets.Add(-5 * time.Hour)
	obs := func(ref string) syncapi.Observation {
		return syncapi.Observation{Provider: "openai", AccountRefHash: ref, Hint: ref, ObservedAt: now.Add(-40 * 24 * time.Hour)}
	}
	event := func(key, ref string, at time.Time) syncapi.Event {
		return syncapi.Event{DedupeKey: key, AccountRefHash: ref, Provider: "openai", Product: "codex", Model: "gpt-5.5",
			OccurredAt: at, Input: 100, CachedInput: 300, Output: 10}
	}
	snap := syncapi.Snapshot{Provider: "openai", AccountRefHash: "plus", Source: "codex-rollout", ObservedAt: now,
		Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: ptr(10.0), ResetsAt: &resets, WindowMinutes: ptr(300)}}}
	// An idle window: its reset is a full window from the reading, so it has
	// not started and its start is no reset.
	idleAt := now.Add(-30 * time.Minute)
	idleResets := idleAt.Add(5 * time.Hour)
	idle := syncapi.Snapshot{Provider: "openai", AccountRefHash: "team", Source: "codex-rollout", ObservedAt: idleAt,
		Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: ptr(0.0), ResetsAt: &idleResets, WindowMinutes: ptr(300)}}}
	req := syncapi.SyncRequest{Version: syncapi.Version,
		Observations: []syncapi.Observation{obs("plus"), obs("team")},
		Events: []syncapi.Event{
			event("codex:1", "plus", start.Add(5*time.Minute)),  // bin 0
			event("codex:2", "plus", start.Add(40*time.Minute)), // bin 2
			event("codex:3", "team", now.Add(-2*time.Hour)),     // another account, today
			event("codex:4", "plus", now.Add(-10*24*time.Hour)), // within 30 days only
			event("codex:5", "plus", now.Add(-35*24*time.Hour)), // older than 30 days
		},
		Snapshots: []syncapi.Snapshot{snap, idle}}
	if st := alice.do("POST", syncapi.PathSync, req, nil); st != http.StatusOK {
		t.Fatalf("sync = %d", st)
	}
	bobReq := syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs("plus")},
		Events: []syncapi.Event{event("codex:b1", "plus", start.Add(40*time.Minute))}}
	if st := bob.do("POST", syncapi.PathSync, bobReq, nil); st != http.StatusOK {
		t.Fatalf("bob sync = %d", st)
	}

	today := now.Add(-3 * time.Hour)
	var ov syncapi.Overview
	path := syncapi.PathOverview + "?" + url.Values{syncapi.QueryToday: {today.Format(time.RFC3339)}}.Encode()
	if st := alice.do("GET", path, nil, &ov); st != http.StatusOK || ov.You == nil {
		t.Fatalf("overview = %d, %+v", st, ov)
	}
	// The window started 4 hours ago, so only codex:3 falls after today.
	if ov.You.Today.Requests != 1 {
		t.Errorf("today requests = %d, want 1", ov.You.Today.Requests)
	}
	if got := ov.You.Last30Day; got.Requests != 4 || got.Input != 400 || got.CachedInput != 1200 || got.Output != 40 || got.CostUSD <= 0 {
		t.Errorf("30 days = %+v, want 4 requests of alice's across both accounts", got)
	}

	var plus, team *syncapi.AccountOverview
	for i := range ov.Accounts {
		switch ov.Accounts[i].Label {
		case "plus":
			plus = &ov.Accounts[i]
		case "team":
			team = &ov.Accounts[i]
		}
	}
	if team == nil || len(team.Buckets) != 1 || team.Buckets[0].Timeline == nil || len(team.Buckets[0].Timeline.Resets) != 0 {
		t.Errorf("idle team account = %+v, want a timeline with no resets", team)
	}
	if plus == nil || len(plus.Buckets) != 1 || plus.Buckets[0].Timeline == nil {
		t.Fatalf("plus account = %+v", plus)
	}
	tl := plus.Buckets[0].Timeline
	// The timeline is the last 5 hours up to now, so it reaches back past
	// the window's start, which it marks as a reset.
	if d := tl.Start.Sub(now.Add(-5 * time.Hour)); d < 0 || d > time.Minute || tl.BinMinutes != 15 || tl.Bins != 20 {
		t.Errorf("timeline = start %v, %d min × %d; want about %v, 15 min × 20", tl.Start, tl.BinMinutes, tl.Bins, now.Add(-5*time.Hour))
	}
	if len(tl.Resets) != 1 || !tl.Resets[0].Equal(start.Round(time.Minute)) {
		t.Errorf("timeline resets = %v, want the window's start %v", tl.Resets, start.Round(time.Minute))
	}
	got := map[[2]string]int64{}
	names := map[string]string{}
	for _, m := range plus.Buckets[0].Members {
		names[m.PersonID] = m.Name
	}
	for _, p := range tl.Points {
		got[[2]string{names[p.PersonID], fmt.Sprint(p.Bin)}] += p.Tokens
	}
	// 65 and 100 minutes into the 15-minute bins.
	want := map[[2]string]int64{{"alice", "4"}: 410, {"alice", "6"}: 410, {"bob", "6"}: 410}
	if len(got) != len(want) {
		t.Errorf("timeline points = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("timeline %v = %d, want %d", k, got[k], v)
		}
	}
}
