package admin_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/server/admin"
	"github.com/KoukeNeko/ShareCodex/internal/server/httpapi"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage/storagetest"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

const password = "correct horse battery"

type browser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newServer(t *testing.T) (*storage.Store, browser) {
	t.Helper()
	store := storagetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	console, err := admin.New(store, log, admin.Config{Password: password, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/admin/", console)
	mux.Handle("GET /dashboard", console)
	mux.Handle("GET /dashboard/usage", console)
	mux.Handle("GET /dashboard/capacity", console)
	mux.Handle("GET /lang/{lang}", console)
	mux.Handle("GET /manifest.webmanifest", console)
	mux.Handle("GET /icons/{name}", console)
	mux.Handle("GET /{$}", console)
	mux.Handle("/", httpapi.New(store, log))

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return store, browser{t: t, base: srv.URL, c: c}
}

func (b browser) get(path string) (int, string) {
	b.t.Helper()
	resp, err := b.c.Get(b.base + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// post submits a form the way a same-origin browser page does.
func (b browser) post(path string, form url.Values) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestRequiresLogin(t *testing.T) {
	_, b := newServer(t)

	if st, _ := b.get("/admin/people"); st != http.StatusSeeOther {
		t.Fatalf("unauthenticated page returned %d, want redirect to login", st)
	}
	if st, body := b.post("/admin/login", url.Values{"password": {"wrong password!"}}); st != http.StatusUnauthorized || !strings.Contains(body, "Wrong password") {
		t.Fatalf("wrong password returned %d", st)
	}
	if st, _ := b.post("/admin/login", url.Values{"password": {password}}); st != http.StatusSeeOther {
		t.Fatalf("login returned %d", st)
	}
	if st, _ := b.get("/admin/people"); st != http.StatusOK {
		t.Fatalf("page after login returned %d", st)
	}

	b.post("/admin/logout", nil)
	if st, _ := b.get("/admin/"); st != http.StatusSeeOther {
		t.Fatalf("page after logout returned %d, want redirect", st)
	}
}

func TestRejectsCrossSiteForms(t *testing.T) {
	store, b := newServer(t)
	b.post("/admin/login", url.Values{"password": {password}})

	req, _ := http.NewRequest(http.MethodPost, b.base+"/admin/people", strings.NewReader("name=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site form returned %d, want 403", resp.StatusCode)
	}
	if persons, _ := store.Persons(context.Background()); len(persons) != 0 {
		t.Fatal("cross-site form created a person")
	}
}

var joinLink = regexp.MustCompile(`value="(http://[^"]+/join/[a-z0-9]+)"`)

func TestPeopleInviteSharesAndRevoke(t *testing.T) {
	ctx := context.Background()
	store, b := newServer(t)
	b.post("/admin/login", url.Values{"password": {password}})

	if st, _ := b.post("/admin/people", url.Values{"name": {"alice"}}); st != http.StatusSeeOther {
		t.Fatalf("add person returned %d", st)
	}
	if st, body := b.post("/admin/people", url.Values{"name": {"alice"}}); st != http.StatusUnprocessableEntity || !strings.Contains(body, "A member with this name already exists") {
		t.Fatalf("duplicate name returned %d", st)
	}
	persons, _ := store.Persons(ctx)
	alice := persons[0]

	st, body := b.post("/admin/people/"+alice.ID+"/invite", nil)
	m := joinLink.FindStringSubmatch(body)
	if st != http.StatusOK || m == nil {
		t.Fatalf("invite returned %d without a join link", st)
	}
	_, code, _ := strings.Cut(m[1], syncapi.PathJoin)
	d, token, err := store.Pair(ctx, code, "alice-laptop", "darwin")
	if err != nil {
		t.Fatalf("the join link shown in the console does not pair: %v", err)
	}

	// The device reports an account, which auto-creates it with alice as a member.
	obs := syncapi.Observation{Provider: "openai", AccountRefHash: "acct", Hint: "al***@example.com", ObservedAt: time.Now()}
	if _, err := store.Ingest(ctx, d, syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	b.post("/admin/people", url.Values{"name": {"bob"}})
	accounts, _ := store.Accounts(ctx)
	acct := accounts[0]
	persons, _ = store.Persons(ctx)
	var bob string
	for _, p := range persons {
		if p.DisplayName == "bob" {
			bob = p.ID
		}
	}

	if st, _ := b.post("/admin/accounts/"+acct.ID+"/label", url.Values{"label": {"Plus A"}}); st != http.StatusSeeOther {
		t.Fatalf("rename returned %d", st)
	}
	form := url.Values{"weight." + alice.ID: {"3"}, "add_person": {bob}, "add_weight": {"1"}}
	if st, _ := b.post("/admin/accounts/"+acct.ID+"/shares", form); st != http.StatusSeeOther {
		t.Fatalf("save shares returned %d", st)
	}
	if st, _ := b.post("/admin/accounts/"+acct.ID+"/shares", url.Values{"weight." + alice.ID: {"-1"}}); st != http.StatusUnprocessableEntity {
		t.Fatalf("negative weight returned %d, want 422", st)
	}
	members, _ := store.Members(ctx, acct.ID)
	got := map[string]float64{}
	for _, m := range members {
		got[m.Name] = m.ShareWeight
	}
	if got["alice"] != 3 || got["bob"] != 1 {
		t.Fatalf("weights = %v, want alice 3, bob 1", got)
	}
	if _, page := b.get("/admin/accounts"); !strings.Contains(page, "Plus A") || !strings.Contains(page, "75%") {
		t.Fatal("accounts page does not show the new label and alice's 75% allotment")
	}

	if st, _ := b.post("/admin/devices/"+d.ID+"/revoke", nil); st != http.StatusSeeOther {
		t.Fatalf("revoke returned %d", st)
	}
	if _, err := store.DeviceByToken(ctx, token); err != storage.ErrUnauthorized {
		t.Fatalf("revoked device still authenticates: %v", err)
	}
	if _, page := b.get("/admin/devices"); !strings.Contains(page, "Revoked") {
		t.Fatal("devices page does not show the revocation")
	}
	if st, _ := b.get("/admin/"); st != http.StatusOK {
		t.Fatalf("overview returned %d", st)
	}
}

func TestOverviewHidesExpiredQuotaEstimates(t *testing.T) {
	ctx := context.Background()
	store, b := newServer(t)
	if st, _ := b.post("/admin/login", url.Values{"password": {password}}); st != http.StatusSeeOther {
		t.Fatalf("login returned %d", st)
	}
	person, err := store.AddPerson(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := store.CreateInvite(ctx, person.ID, storage.InviteTTL)
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := store.Pair(ctx, code, "alice-laptop", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	used, minutes := 80.0, 300
	resets := time.Now().UTC().Add(-time.Minute)
	_, err = store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version,
		Snapshots: []syncapi.Snapshot{{Provider: "anthropic", AccountRefHash: "acct", Source: "claude-statusline", ObservedAt: resets.Add(-time.Hour),
			Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &minutes}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, page := b.get("/admin/"); st != http.StatusOK || !strings.Contains(page, "—") || strings.Contains(page, "80%") || strings.Contains(page, `class="meter"`) || strings.Contains(page, "Estimated usage") {
		t.Fatalf("expired quota page = %d, expected unknown without estimates: %s", st, page)
	}
}

func TestRejectsShortPassword(t *testing.T) {
	if _, err := admin.New(nil, nil, admin.Config{Password: "short", PublicURL: "https://x"}); err == nil {
		t.Fatal("a short admin password must be rejected at startup")
	}
}

func TestLanguageSwitch(t *testing.T) {
	_, b := newServer(t)
	b.post("/admin/login", url.Values{"password": {password}})

	if _, page := b.get("/admin/people"); !strings.Contains(page, "Add member") || !strings.Contains(page, `lang="en"`) {
		t.Fatal("the console must default to English")
	}

	resp, err := b.c.Get(b.base + "/lang/zh-TW?next=/admin/people")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/people" {
		t.Fatalf("switch returned %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, page := b.get("/admin/people"); !strings.Contains(page, "新增成員") || !strings.Contains(page, `lang="zh-Hant-TW"`) {
		t.Fatal("the chosen language did not stick")
	}
	if st, body := b.post("/admin/people", url.Values{"name": {""}}); st != http.StatusUnprocessableEntity || !strings.Contains(body, "請輸入名稱") {
		t.Fatalf("validation errors must follow the language, got %d", st)
	}

	for _, next := range []string{"https://evil.example/", "//evil.example/admin/", "/elsewhere"} {
		resp, err := b.c.Get(b.base + "/lang/en?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); loc != "/admin/" {
			t.Errorf("next=%q redirected to %q, want /admin/", next, loc)
		}
	}
	if st, _ := b.get("/lang/fr"); st != http.StatusNotFound {
		t.Errorf("unknown language returned %d, want 404", st)
	}
}

// seedUsage pairs alice's laptop, signs it into a Claude account and
// uploads a 5-hour reading with two requests on it: one to the quota's own
// model and one to a third-party model through OpenCodex.
func seedUsage(t *testing.T, store *storage.Store) storage.Device {
	t.Helper()
	ctx := context.Background()
	person, err := store.AddPerson(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := store.CreateInvite(ctx, person.ID, storage.InviteTTL)
	if err != nil {
		t.Fatal(err)
	}
	device, token, err := store.Pair(ctx, code, "alice-laptop", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	// Authenticating is what marks a device seen.
	if _, err := store.DeviceByToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	used, minutes := 40.0, 300
	resets := now.Add(2 * time.Hour)
	event := func(key, model, gateway string, thirdParty bool) syncapi.Event {
		return syncapi.Event{DedupeKey: key, AccountRefHash: "acct", Provider: "anthropic", Product: "claude-code",
			Model: model, Gateway: gateway, ThirdParty: thirdParty, OccurredAt: now.Add(-10 * time.Minute),
			Input: 1000, CachedInput: 3000, Output: 500}
	}
	_, err = store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version,
		Observations: []syncapi.Observation{{Provider: "anthropic", AccountRefHash: "acct", Hint: "al***@example.com", ObservedAt: now.Add(-time.Minute)}},
		Events: []syncapi.Event{
			event("e1", "claude-opus-5-5", "", false),
			event("e2", "ocx-claude-native--gpt-6-sol", "opencodex", true),
		},
		Snapshots: []syncapi.Snapshot{{Provider: "anthropic", AccountRefHash: "acct", Source: "claude-oauth-usage", ObservedAt: now.Add(-time.Minute),
			Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &minutes}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return device
}

// A later reading can report the account under another masked identifier
// than the one its label was first taken from; the label is still the
// masked email.
func TestPublicDashboardHidesMaskedLabelAfterHintChanges(t *testing.T) {
	store, admin := newServer(t)
	device := seedUsage(t, store)
	_, err := store.Ingest(context.Background(), device, syncapi.SyncRequest{Version: syncapi.Version,
		Observations: []syncapi.Observation{{Provider: "anthropic", AccountRefHash: "acct", Hint: "or***", ObservedAt: time.Now()}}})
	if err != nil {
		t.Fatal(err)
	}
	admin.post("/admin/login", url.Values{"password": {password}})
	admin.post("/admin/settings", url.Values{"public_dashboard": {"on"}})
	jar, _ := cookiejar.New(nil)
	visitor := browser{t: t, base: admin.base, c: &http.Client{Jar: jar, CheckRedirect: admin.c.CheckRedirect}}
	for _, path := range []string{"/dashboard", "/dashboard/usage"} {
		st, page := visitor.get(path)
		if st != http.StatusOK || !strings.Contains(page, "Account 1") || strings.Contains(page, "al***@example.com") {
			t.Errorf("%s returned %d without the numbered account, or with the masked email", path, st)
		}
	}
}

func TestPublicDashboard(t *testing.T) {
	store, admin := newServer(t)
	seedUsage(t, store)
	jar, _ := cookiejar.New(nil)
	visitor := browser{t: t, base: admin.base, c: &http.Client{Jar: jar, CheckRedirect: admin.c.CheckRedirect}}

	location := func(b browser, path string) string {
		t.Helper()
		resp, err := b.c.Get(b.base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}

	if st, _ := visitor.get("/dashboard"); st != http.StatusNotFound {
		t.Fatalf("unpublished dashboard returned %d to a visitor, want 404", st)
	}
	if loc := location(visitor, "/"); loc != "/admin/" {
		t.Fatalf("root redirected to %q before publishing, want /admin/", loc)
	}

	admin.post("/admin/login", url.Values{"password": {password}})
	if st, page := admin.get("/dashboard"); st != http.StatusOK || !strings.Contains(page, "Preview") {
		t.Fatalf("an admin's preview returned %d", st)
	}
	if st, _ := admin.post("/admin/settings", url.Values{"public_dashboard": {"on"}}); st != http.StatusSeeOther {
		t.Fatalf("publishing returned %d", st)
	}
	if _, page := admin.get("/admin/settings"); !strings.Contains(page, admin.base+"/dashboard") {
		t.Fatal("settings do not link the published dashboard")
	}

	st, page := visitor.get("/dashboard")
	if st != http.StatusOK {
		t.Fatalf("published dashboard returned %d", st)
	}
	for _, want := range []string{"alice", "Account 1", "40%", "claude-opus-5-5", "gpt-6-sol", "OCX", "<svg", `href="/dashboard/usage"`} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	for _, private := range []string{"al***@example.com", "alice-laptop", "Sign out"} {
		if strings.Contains(page, private) {
			t.Errorf("dashboard shows %q, which only the console may", private)
		}
	}
	// The usage report covers the period asked for, without email addresses.
	st, page = visitor.get("/dashboard/usage?period=24h")
	if st != http.StatusOK || !strings.Contains(page, "4.5K") || !strings.Contains(page, "Account 1") || strings.Contains(page, "al***@example.com") {
		t.Errorf("dashboard usage for 24 hours returned %d without alice's tokens and the numbered account, or with an email", st)
	}
	// Nothing on the public pages leads into /admin.
	if strings.Contains(page, `href="/admin`) {
		t.Error("the public usage page links into /admin")
	}
	if loc := location(visitor, "/"); loc != "/dashboard" {
		t.Fatalf("root redirected to %q after publishing, want /dashboard", loc)
	}

	admin.post("/admin/settings", url.Values{})
	for _, path := range []string{"/dashboard", "/dashboard/usage"} {
		if st, _ := visitor.get(path); st != http.StatusNotFound {
			t.Fatalf("unpublished again, %s returned %d, want 404", path, st)
		}
	}
}

// A member's usage page holds that member's requests alone, with their
// devices named in the console and left out of the public dashboard.
func TestMemberUsagePages(t *testing.T) {
	ctx := context.Background()
	store, admin := newServer(t)
	seedUsage(t, store)
	bobPerson, err := store.AddPerson(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := store.CreateInvite(ctx, bobPerson.ID, storage.InviteTTL)
	if err != nil {
		t.Fatal(err)
	}
	bobDevice, _, err := store.Pair(ctx, code, "bob-phone", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = store.Ingest(ctx, bobDevice, syncapi.SyncRequest{Version: syncapi.Version,
		Events: []syncapi.Event{{DedupeKey: "b1", AccountRefHash: "acct", Provider: "anthropic", Product: "claude-code",
			Model: "claude-sonnet-5-5", OccurredAt: now.Add(-10 * time.Minute), Input: 2000, Output: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	var aliceID string
	people, err := store.Persons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		if p.DisplayName == "alice" {
			aliceID = p.ID
		}
	}
	member := "&member=" + aliceID
	// The pages link with the ampersand escaped.
	linked := "&amp;member=" + aliceID

	admin.post("/admin/login", url.Values{"password": {password}})
	if _, page := admin.get("/admin/people"); !strings.Contains(page, "/admin/usage?member="+aliceID) {
		t.Error("members page does not link alice's usage")
	}
	// Everyone's page links each member to theirs.
	if _, page := admin.get("/admin/usage?period=24h"); !strings.Contains(page, "period=24h"+linked) || !strings.Contains(page, "claude-sonnet-5-5") {
		t.Error("usage page does not link alice's usage, or lacks bob's model")
	}
	st, page := admin.get("/admin/usage?period=24h" + member)
	if st != http.StatusOK {
		t.Fatalf("alice's usage returned %d", st)
	}
	for _, want := range []string{"alice", "All members", "alice-laptop", "gpt-6-sol", "OCX", "4.0K", "period=7d" + linked} {
		if !strings.Contains(page, want) {
			t.Errorf("alice's usage lacks %q", want)
		}
	}
	for _, other := range []string{"bob", "bob-phone", "claude-sonnet-5-5"} {
		if strings.Contains(page, other) {
			t.Errorf("alice's usage shows %q, which is not hers", other)
		}
	}
	if st, _ := admin.get("/admin/usage?member=no-such-member"); st != http.StatusNotFound {
		t.Errorf("unknown member returned %d, want 404", st)
	}

	jar, _ := cookiejar.New(nil)
	visitor := browser{t: t, base: admin.base, c: &http.Client{Jar: jar, CheckRedirect: admin.c.CheckRedirect}}
	if st, _ := visitor.get("/dashboard/usage?member=" + aliceID); st != http.StatusNotFound {
		t.Fatalf("unpublished dashboard returned %d for a member's usage, want 404", st)
	}
	admin.post("/admin/settings", url.Values{"public_dashboard": {"on"}})
	st, page = visitor.get("/dashboard/usage?period=24h" + member)
	if st != http.StatusOK {
		t.Fatalf("alice's public usage returned %d", st)
	}
	for _, want := range []string{"alice", "All members", "Account 1", "gpt-6-sol", "4.0K"} {
		if !strings.Contains(page, want) {
			t.Errorf("alice's public usage lacks %q", want)
		}
	}
	for _, private := range []string{"al***@example.com", "alice-laptop", "bob", "claude-sonnet-5-5"} {
		if strings.Contains(page, private) {
			t.Errorf("alice's public usage shows %q", private)
		}
	}
	if st, _ := visitor.get("/dashboard/usage?member=no-such-member"); st != http.StatusNotFound {
		t.Errorf("unknown member returned %d on the dashboard, want 404", st)
	}
}

func TestConsoleShowsUsageDetails(t *testing.T) {
	store, b := newServer(t)
	seedUsage(t, store)
	b.post("/admin/login", url.Values{"password": {password}})

	_, page := b.get("/admin/")
	for _, want := range []string{"al***@example.com", "In use", "alice-laptop", "Third-party models", "<svg", "4.5K"} {
		if !strings.Contains(page, want) {
			t.Errorf("overview lacks %q", want)
		}
	}

	_, page = b.get("/admin/usage?period=24h")
	// One quota request: 1,000 input and 3,000 cached tokens, 500 output;
	// the third-party request is listed apart and not in the totals.
	for _, want := range []string{"alice", "Claude", "4.0K", "75%", "500", "gpt-6-sol", "OCX"} {
		if !strings.Contains(page, want) {
			t.Errorf("usage page lacks %q", want)
		}
	}

	if _, page = b.get("/admin/devices"); !strings.Contains(page, "Online") || !strings.Contains(page, "Claude · al***@example.com") {
		t.Error("devices page does not show the device online and signed in to the account")
	}
	if _, page = b.get("/admin/people"); !strings.Contains(page, "4.5K") {
		t.Error("members page does not show alice's 30-day tokens")
	}
}

// Each delete runs against a full set of records: usage, quota readings,
// sign-ins, memberships and devices.
func TestDeleteAccount(t *testing.T) {
	ctx := context.Background()
	store, b := newServer(t)
	seedUsage(t, store)
	b.post("/admin/login", url.Values{"password": {password}})
	accounts, _ := store.Accounts(ctx)

	if st, _ := b.post("/admin/accounts/"+accounts[0].ID+"/delete", nil); st != http.StatusSeeOther {
		t.Fatalf("delete account returned %d", st)
	}
	if accounts, _ = store.Accounts(ctx); len(accounts) != 0 {
		t.Fatalf("accounts after delete = %+v", accounts)
	}
	if _, page := b.get("/admin/usage"); strings.Contains(page, "claude-opus-5-5") {
		t.Error("the deleted account's usage is still reported")
	}
	if persons, _ := store.Persons(ctx); len(persons) != 1 {
		t.Error("deleting an account removed its member")
	}
}

func TestDeleteMember(t *testing.T) {
	ctx := context.Background()
	store, b := newServer(t)
	seedUsage(t, store)
	b.post("/admin/login", url.Values{"password": {password}})
	persons, _ := store.Persons(ctx)
	alice := persons[0]

	if st, _ := b.post("/admin/people/"+alice.ID+"/delete", nil); st != http.StatusSeeOther {
		t.Fatalf("delete member returned %d", st)
	}
	if persons, _ = store.Persons(ctx); len(persons) != 0 {
		t.Fatalf("people after delete = %+v", persons)
	}
	if devices, _ := store.Devices(ctx); len(devices) != 0 {
		t.Fatalf("the member's devices remain: %+v", devices)
	}
	if _, page := b.get("/admin/usage"); strings.Contains(page, "claude-opus-5-5") {
		t.Error("the deleted member's usage is still reported")
	}
	if st, _ := b.post("/admin/people/"+alice.ID+"/delete", nil); st != http.StatusSeeOther {
		t.Fatalf("deleting a member twice returned %d", st)
	}
}

func TestAccountOrder(t *testing.T) {
	ctx := context.Background()
	store, b := newServer(t)
	seedUsage(t, store)
	persons, _ := store.Persons(ctx)
	code, _, _ := store.CreateInvite(ctx, persons[0].ID, storage.InviteTTL)
	device, _, err := store.Pair(ctx, code, "alice-pc", "windows")
	if err != nil {
		t.Fatal(err)
	}
	obs := syncapi.Observation{Provider: "openai", AccountRefHash: "codex", Hint: "co***@example.com", ObservedAt: time.Now()}
	if _, err := store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	b.post("/admin/login", url.Values{"password": {password}})
	accounts, _ := store.Accounts(ctx)
	if len(accounts) != 2 || accounts[0].Provider != "anthropic" {
		t.Fatalf("accounts before arranging = %+v, want Claude first", accounts)
	}

	form := url.Values{"id": {accounts[1].ID, "gone", accounts[0].ID}}
	if st, _ := b.post("/admin/accounts/order", form); st != http.StatusSeeOther {
		t.Fatalf("saving the order returned %d", st)
	}
	arranged, _ := store.Accounts(ctx)
	if arranged[0].ID != accounts[1].ID || arranged[1].ID != accounts[0].ID {
		t.Fatalf("accounts after arranging = %+v, want Codex first", arranged)
	}
	_, page := b.get("/admin/")
	if strings.Index(page, "co***@example.com") > strings.Index(page, "al***@example.com") {
		t.Error("the overview does not follow the arranged order")
	}
	if !strings.Contains(page, `data-five-hour="40"`) || !strings.Contains(page, `data-weekly="-1"`) {
		t.Error("cards lack the window use the sort control orders them by")
	}
}

// The public dashboard can be installed on a phone: its pages link a
// manifest Chrome accepts, and every icon the manifest names is served.
func TestDashboardIsInstallable(t *testing.T) {
	store, b := newServer(t)
	if err := store.SetPublicDashboard(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, page := b.get("/dashboard"); !strings.Contains(page, `rel="manifest" href="/manifest.webmanifest"`) {
		t.Fatal("the dashboard does not link its manifest")
	}
	resp, err := b.c.Get(b.base + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/manifest+json" || m.Name == "" || m.StartURL != "/dashboard" || m.Display != "standalone" {
		t.Fatalf("manifest = %+v (%s)", m, ct)
	}
	sizes := map[string]bool{}
	for _, icon := range m.Icons {
		sizes[icon.Sizes] = true
		if st, _ := b.get(icon.Src); st != http.StatusOK {
			t.Errorf("%s returned %d", icon.Src, st)
		}
	}
	if !sizes["192x192"] || !sizes["512x512"] {
		t.Errorf("icon sizes = %v, want 192 and 512", sizes)
	}
	if st, _ := b.get("/icons/../admin.go"); st == http.StatusOK {
		t.Error("the icon route serves files outside the icons")
	}
}

func seededAccountID(t *testing.T, store *storage.Store) string {
	t.Helper()
	accounts, err := store.Accounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	return accounts[0].ID
}

// The form's times name instants with an offset, the browser's own; the
// server's zone has no say in them.
func TestPlanFormHonoursTheSubmittedOffset(t *testing.T) {
	store, b := newServer(t)
	seedUsage(t, store)
	id := seededAccountID(t, store)
	b.post("/admin/login", url.Values{"password": {password}})

	st, _ := b.post("/admin/accounts/"+id+"/plans", url.Values{"plan_type": {"max 5x"}, "precision": {"confirmed"},
		"effective_at": {"2026-09-30T01:10:00+08:00"}, "ended_at": {"2026-09-30T23:29:00+08:00"}})
	if st != http.StatusSeeOther {
		t.Fatalf("saving a plan returned %d, want a redirect", st)
	}
	plans, err := store.PlanHistory(context.Background(), id)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans = %v, %v", plans, err)
	}
	if want := time.Date(2026, 9, 29, 17, 10, 0, 0, time.UTC); !plans[0].EffectiveAt.Equal(want) {
		t.Errorf("effective at %v, want %v", plans[0].EffectiveAt, want)
	}
	if want := time.Date(2026, 9, 30, 15, 29, 0, 0, time.UTC); plans[0].EndedAt == nil || !plans[0].EndedAt.Equal(want) {
		t.Errorf("ended at %v, want %v", plans[0].EndedAt, want)
	}

	_, page := b.get("/admin/accounts/" + id + "/plans")
	if !strings.Contains(page, `<select name="plan_type"`) || !strings.Contains(page, `data-instant="effective_at"`) {
		t.Error("the form of a Claude account lacks a plan choice or the field the script converts")
	}
}

// A form that cannot be saved comes back with why, not a silent redirect.
func TestPlanFormShowsWhyItWasRefused(t *testing.T) {
	store, b := newServer(t)
	seedUsage(t, store)
	id := seededAccountID(t, store)
	b.post("/admin/login", url.Values{"password": {password}})
	path := "/admin/accounts/" + id + "/plans"
	save := func(form url.Values) (int, string) {
		form.Set("precision", "confirmed")
		return b.post(path, form)
	}

	if st, _ := save(url.Values{"plan_type": {"pro"}, "effective_at": {"2026-09-01T00:00:00Z"}, "ended_at": {"2026-10-01T00:00:00Z"}}); st != http.StatusSeeOther {
		t.Fatalf("first interval returned %d", st)
	}
	tests := []struct {
		name string
		form url.Values
		want string
	}{
		{"overlap", url.Values{"plan_type": {"max 5x"}, "effective_at": {"2026-09-15T00:00:00Z"}}, "This interval overlaps an existing one"},
		{"no offset", url.Values{"plan_type": {"max 5x"}, "effective_at": {"2026-10-02T01:10"}}, "Enter a valid time"},
		{"no time", url.Values{"plan_type": {"max 5x"}}, "Enter a valid time"},
		{"bad end", url.Values{"plan_type": {"max 5x"}, "effective_at": {"2026-10-02T00:00:00Z"}, "ended_at": {"later"}}, "Enter a valid time"},
		{"end before start", url.Values{"plan_type": {"max 5x"}, "effective_at": {"2026-10-02T00:00:00Z"}, "ended_at": {"2026-10-01T00:00:00Z"}}, "The end must be after the start"},
		{"no plan", url.Values{"plan_type": {" "}, "effective_at": {"2026-10-02T00:00:00Z"}}, "Enter a plan"},
	}
	for _, tt := range tests {
		tt.form.Set("reason", "kept "+tt.name)
		st, page := save(tt.form)
		if st != http.StatusUnprocessableEntity || !strings.Contains(page, tt.want) {
			t.Errorf("%s: returned %d, want 422 saying %q", tt.name, st, tt.want)
		}
		if !strings.Contains(page, "kept "+tt.name) {
			t.Errorf("%s: the form lost what was typed", tt.name)
		}
	}
	if plans, _ := store.PlanHistory(context.Background(), id); len(plans) != 1 {
		t.Errorf("a refused form saved: %d intervals, want 1", len(plans))
	}
	if st, _ := b.post("/admin/accounts/nope/plans", url.Values{"plan_type": {"pro"}, "effective_at": {"2026-10-02T00:00:00Z"}}); st != http.StatusNotFound {
		t.Errorf("an unknown account returned %d, want 404", st)
	}
}

func TestCapacityPageIsTranslated(t *testing.T) {
	store, b := newServer(t)
	seedUsage(t, store)
	id := seededAccountID(t, store)
	b.post("/admin/login", url.Values{"password": {password}})

	st, page := b.get("/admin/accounts/" + id + "/capacity?period=7d")
	if st != http.StatusOK {
		t.Fatalf("capacity page returned %d", st)
	}
	for _, want := range []string{"Calibration", "Shared Max 5x", "Insufficient data", "Recorded demand stops where"} {
		if !strings.Contains(page, want) {
			t.Errorf("capacity page lacks %q", want)
		}
	}
	b.get("/lang/zh-TW?next=/admin/")
	_, page = b.get("/admin/accounts/" + id + "/capacity")
	for _, want := range []string{"校準", "共用 Max 5x", "資料不足"} {
		if !strings.Contains(page, want) {
			t.Errorf("zh-TW capacity page lacks %q", want)
		}
	}
	if st, _ := b.get("/admin/accounts/nope/capacity"); st != http.StatusNotFound {
		t.Errorf("an unknown account returned %d, want 404", st)
	}
}

// The page lays out every figure of a report that has samples, replays and
// a week; a template that cannot would stop part way.
func TestCapacityPageShowsMeasuredPlans(t *testing.T) {
	ctx := context.Background()
	store, b := newServer(t)
	device := seedUsage(t, store)
	id := seededAccountID(t, store)
	now := time.Now().UTC()
	if _, err := store.SavePlanInterval(ctx, account.PlanInterval{AccountID: id, PlanType: "max 5x", EffectiveAt: now.Add(-20 * 24 * time.Hour), Source: "admin"}); err != nil {
		t.Fatal(err)
	}
	five, week := 300, 10080
	var events []syncapi.Event
	var snapshots []syncapi.Snapshot
	for i := range 2 {
		start := now.Add(-time.Duration(6*i+8) * time.Hour)
		resets, observed, used := start.Add(5*time.Hour), start.Add(2*time.Hour), 20.0
		events = append(events, syncapi.Event{DedupeKey: "w" + strconv.Itoa(i), AccountRefHash: "acct", Provider: "anthropic", Product: "claude-code",
			Model: "claude-opus-5-5", OccurredAt: start.Add(30 * time.Minute), Output: 2_000_000})
		snapshots = append(snapshots, syncapi.Snapshot{Provider: "anthropic", AccountRefHash: "acct", Source: "claude-oauth-usage", ObservedAt: observed,
			Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &five}}})
	}
	weekUsed, weekResets := 30.0, now.Add(4*24*time.Hour)
	snapshots = append(snapshots, syncapi.Snapshot{Provider: "anthropic", AccountRefHash: "acct", Source: "claude-oauth-usage", ObservedAt: now.Add(-time.Hour),
		Buckets: []syncapi.Bucket{{Key: "weekly", UsedPercent: &weekUsed, ResetsAt: &weekResets, WindowMinutes: &week}}})
	if _, err := store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version, Events: events, Snapshots: snapshots}); err != nil {
		t.Fatal(err)
	}
	b.post("/admin/login", url.Values{"password": {password}})

	st, page := b.get("/admin/accounts/" + id + "/capacity?period=24h")
	if st != http.StatusOK {
		t.Fatalf("capacity page returned %d", st)
	}
	for _, want := range []string{"Measured", "Derived", "Reached 100%", "Recorded demand stops where", "Peak"} {
		if !strings.Contains(page, want) {
			t.Errorf("capacity page lacks %q", want)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(page), "</html>") {
		t.Error("the capacity page was cut short")
	}
}

func publishDashboard(t *testing.T, admin browser) browser {
	t.Helper()
	admin.post("/admin/login", url.Values{"password": {password}})
	admin.post("/admin/settings", url.Values{"public_dashboard": {"on"}})
	jar, _ := cookiejar.New(nil)
	return browser{t: t, base: admin.base, c: &http.Client{Jar: jar, CheckRedirect: admin.c.CheckRedirect}}
}

func TestDashboardCapacityAccess(t *testing.T) {
	store, admin := newServer(t)
	seedUsage(t, store)
	id := seededAccountID(t, store)
	jar, _ := cookiejar.New(nil)
	visitor := browser{t: t, base: admin.base, c: &http.Client{Jar: jar, CheckRedirect: admin.c.CheckRedirect}}

	if st, _ := visitor.get("/dashboard/capacity"); st != http.StatusNotFound {
		t.Fatalf("unpublished capacity returned %d to a visitor, want 404", st)
	}
	admin.post("/admin/login", url.Values{"password": {password}})
	if st, page := admin.get("/dashboard/capacity"); st != http.StatusOK || !strings.Contains(page, "Preview") {
		t.Fatalf("an admin's preview returned %d without the notice", st)
	}
	admin.post("/admin/settings", url.Values{"public_dashboard": {"on"}})

	st, page := visitor.get("/dashboard/capacity?period=24h")
	if st != http.StatusOK {
		t.Fatalf("published capacity returned %d", st)
	}
	for _, want := range []string{`href="/dashboard/capacity" class="active"`, "Account 1",
		`account=` + id + `&amp;period=30d"`, `account=` + id + `&amp;period=24h" class="active"`} {
		if !strings.Contains(page, want) {
			t.Errorf("capacity page lacks %q", want)
		}
	}
	if strings.Contains(page, `href="/admin`) {
		t.Error("the public capacity page links into /admin")
	}
	if _, home := visitor.get("/dashboard"); !strings.Contains(home, `href="/dashboard/capacity"`) {
		t.Error("the dashboard does not link the capacity page")
	}
	if st, _ := visitor.get("/dashboard/capacity?account=nope"); st != http.StatusNotFound {
		t.Errorf("an unknown account returned %d, want 404", st)
	}

	admin.post("/admin/settings", url.Values{})
	if st, _ := visitor.get("/dashboard/capacity"); st != http.StatusNotFound {
		t.Errorf("unpublished again, capacity returned %d, want 404", st)
	}
}

// Without an account the page opens on the first Claude account of the
// public order; a period switch keeps the account.
func TestDashboardCapacityPicksAnAccount(t *testing.T) {
	ctx := context.Background()
	store, admin := newServer(t)
	seedUsage(t, store)
	persons, _ := store.Persons(ctx)
	code, _, _ := store.CreateInvite(ctx, persons[0].ID, storage.InviteTTL)
	device, _, err := store.Pair(ctx, code, "alice-pc", "windows")
	if err != nil {
		t.Fatal(err)
	}
	obs := syncapi.Observation{Provider: "openai", AccountRefHash: "codex", Hint: "co***@example.com", ObservedAt: time.Now()}
	if _, err := store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version, Observations: []syncapi.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	accounts, _ := store.Accounts(ctx)
	claude, codex := accounts[0], accounts[1]
	if claude.Provider != "anthropic" || codex.Provider != "openai" {
		t.Fatalf("accounts = %+v, want Claude then Codex", accounts)
	}
	if err := store.SetAccountOrder(ctx, []string{codex.ID, claude.ID}); err != nil {
		t.Fatal(err)
	}
	visitor := publishDashboard(t, admin)

	_, page := visitor.get("/dashboard/capacity")
	if !regexp.MustCompile(`account=` + claude.ID + `&amp;period=\w+" class="active">Account 2<`).MatchString(page) {
		t.Error("the page did not open on the Claude account")
	}
	_, page = visitor.get("/dashboard/capacity?account=" + codex.ID + "&period=30d")
	for _, want := range []string{"Only Claude plans are modelled", `account=` + codex.ID + `&amp;period=30d" class="active"`,
		`account=` + codex.ID + `&amp;period=7d"`, `account=` + claude.ID + `&amp;period=30d"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Codex capacity page lacks %q", want)
		}
	}
}

// The dashboard names the account as the overview does and leaves out the
// reason an admin gave a plan; the console keeps both.
func TestDashboardCapacityHidesWhatOnlyTheConsoleShows(t *testing.T) {
	ctx := context.Background()
	store, admin := newServer(t)
	seedUsage(t, store)
	id := seededAccountID(t, store)
	if _, err := store.SavePlanInterval(ctx, account.PlanInterval{AccountID: id, PlanType: "max 5x", Precision: account.PrecisionInferred,
		EffectiveAt: time.Now().Add(-48 * time.Hour), Reason: "billing-dispute-7731", Source: "admin"}); err != nil {
		t.Fatal(err)
	}
	visitor := publishDashboard(t, admin)

	// The second request is served from the cache.
	for range 2 {
		st, page := visitor.get("/dashboard/capacity?account=" + id)
		if st != http.StatusOK || !strings.Contains(page, "max 5x") || !strings.Contains(page, "Inferred") {
			t.Fatalf("capacity returned %d without the plan interval", st)
		}
		for _, private := range []string{"billing-dispute-7731", "al***@example.com", "alice-laptop"} {
			if strings.Contains(page, private) {
				t.Errorf("capacity page shows %q, which only the console may", private)
			}
		}
	}
	_, page := admin.get("/admin/accounts/" + id + "/capacity")
	if !strings.Contains(page, "al***@example.com") || !strings.Contains(page, "Inferred") {
		t.Error("the console's capacity page lost its account label")
	}
	if _, page := admin.get("/admin/accounts/" + id + "/plans"); !strings.Contains(page, "billing-dispute-7731") {
		t.Error("the plan history lost its reason")
	}
	admin.get("/lang/zh-TW?next=/admin/")
	if _, page := admin.get("/admin/accounts/" + id + "/plans"); !strings.Contains(page, "推定") {
		t.Error("the plan history does not translate the precision")
	}
}
