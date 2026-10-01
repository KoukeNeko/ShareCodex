package admin_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

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

	resp, err := b.c.Get(b.base + "/admin/lang/zh-TW?next=/admin/people")
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
		resp, err := b.c.Get(b.base + "/admin/lang/en?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); loc != "/admin/" {
			t.Errorf("next=%q redirected to %q, want /admin/", next, loc)
		}
	}
	if st, _ := b.get("/admin/lang/fr"); st != http.StatusNotFound {
		t.Errorf("unknown language returned %d, want 404", st)
	}
}

// seedUsage pairs alice's laptop, signs it into a Claude account and
// uploads a 5-hour reading with two requests on it: one to the quota's own
// model and one to a third-party model through OpenCodex.
func seedUsage(t *testing.T, store *storage.Store) {
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
	if st, page := admin.get("/admin/dashboard"); st != http.StatusOK || !strings.Contains(page, "Preview") {
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
	for _, want := range []string{"alice", "Account 1", "40%", "claude-opus-5-5", "gpt-6-sol", "OCX", "<svg"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	for _, private := range []string{"al***@example.com", "alice-laptop", "Sign out"} {
		if strings.Contains(page, private) {
			t.Errorf("dashboard shows %q, which only the console may", private)
		}
	}
	if loc := location(visitor, "/"); loc != "/dashboard" {
		t.Fatalf("root redirected to %q after publishing, want /dashboard", loc)
	}

	admin.post("/admin/settings", url.Values{})
	if st, _ := visitor.get("/dashboard"); st != http.StatusNotFound {
		t.Fatalf("unpublished again, the dashboard returned %d, want 404", st)
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
