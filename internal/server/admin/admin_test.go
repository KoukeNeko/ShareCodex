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
