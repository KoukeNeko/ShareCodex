// Package admin is the server's web console: people and join links,
// accounts and quota shares, and devices. It replaces the former admin CLI
// so a deployment needs no shell access to the container.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/identity"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/server/query"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// MinPasswordLength guards the console, which is reachable wherever the
// sync API is.
const MinPasswordLength = 12

const (
	sessionCookie = "sharecodex_admin"
	sessionTTL    = 12 * time.Hour
	maxFormBytes  = 64 << 10
)

//go:embed templates/*.html
var templateFS embed.FS

type Config struct {
	// Password is the shared admin password (ADMIN_PASSWORD).
	Password string
	// PublicURL is the base URL members reach the server at; join links and
	// the Secure cookie flag derive from it.
	PublicURL string
	// Version is the server's release, shown in the page footer.
	Version string
}

type Console struct {
	store     *storage.Store
	log       *slog.Logger
	password  [32]byte
	publicURL string
	version   string
	pages     map[string]*template.Template

	mu       sync.Mutex
	sessions map[string]time.Time
	// dash caches the public dashboard's overview, which anyone can ask
	// for.
	dash dashboardCache
	// loginMu serialises failed-login delays so guessing is slow.
	loginMu sync.Mutex
}

func New(store *storage.Store, log *slog.Logger, cfg Config) (http.Handler, error) {
	if len(cfg.Password) < MinPasswordLength {
		return nil, errors.New("ADMIN_PASSWORD must be at least 12 characters")
	}
	if cfg.PublicURL == "" {
		return nil, errors.New("PUBLIC_URL is not set; join links need the address members reach the server at")
	}
	pages, err := parsePages()
	if err != nil {
		return nil, err
	}
	c := &Console{
		store:     store,
		log:       log,
		password:  sha256.Sum256([]byte(cfg.Password)),
		publicURL: strings.TrimRight(cfg.PublicURL, "/"),
		version:   cfg.Version,
		pages:     pages,
		sessions:  map[string]time.Time{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /lang/{lang}", c.setLang)
	mux.HandleFunc("GET /admin/login", c.loginPage)
	mux.HandleFunc("POST /admin/login", c.login)
	mux.HandleFunc("POST /admin/logout", c.logout)
	mux.HandleFunc("GET /admin/{$}", c.authed(c.overview))
	mux.HandleFunc("GET /admin/people", c.authed(c.people))
	mux.HandleFunc("POST /admin/people", c.authed(c.addPerson))
	mux.HandleFunc("POST /admin/people/{id}/invite", c.authed(c.invite))
	mux.HandleFunc("POST /admin/people/{id}/delete", c.authed(c.deletePerson))
	mux.HandleFunc("GET /admin/accounts", c.authed(c.accounts))
	mux.HandleFunc("POST /admin/accounts/{id}/label", c.authed(c.setLabel))
	mux.HandleFunc("POST /admin/accounts/{id}/shares", c.authed(c.setShares))
	mux.HandleFunc("GET /admin/accounts/{id}/plans", c.authed(c.accountPlans))
	mux.HandleFunc("POST /admin/accounts/{id}/plans", c.authed(c.saveAccountPlan))
	mux.HandleFunc("POST /admin/accounts/{id}/plans/{pid}/delete", c.authed(c.deleteAccountPlan))
	mux.HandleFunc("GET /admin/accounts/{id}/capacity", c.authed(c.accountCapacity))
	mux.HandleFunc("POST /admin/accounts/{id}/delete", c.authed(c.deleteAccount))
	mux.HandleFunc("POST /admin/accounts/order", c.authed(c.setAccountOrder))
	mux.HandleFunc("GET /admin/devices", c.authed(c.devices))
	mux.HandleFunc("POST /admin/devices/{id}/revoke", c.authed(c.revoke))
	mux.HandleFunc("GET /admin/usage", c.authed(c.usage))
	mux.HandleFunc("GET /admin/settings", c.authed(c.settings))
	mux.HandleFunc("POST /admin/settings", c.authed(c.saveSettings))
	mux.HandleFunc("GET /{$}", c.root)
	mux.HandleFunc("GET "+dashboardPath, c.dashboard)
	mux.HandleFunc("GET "+dashboardUsagePath, c.dashboardUsage)
	mux.HandleFunc("GET "+dashboardCapacityPath, c.dashboardCapacity)
	mux.HandleFunc("GET /manifest.webmanifest", c.manifest)
	mux.HandleFunc("GET /icons/{name}", c.icon)

	// Forms post with the session cookie; reject cross-site requests.
	return http.NewCrossOriginProtection().Handler(mux), nil
}

func parsePages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"pct":      percent,
		"weight":   func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) },
		"provider": providerName,
		"bucket":   bucketName,
		"tf":       fmt.Sprintf,
		"tfh":      tfHTML,
		"when":     func(t any) template.HTML { return timeHTML(t, "datetime") },
		"timeAs":   func(format string, t any) template.HTML { return timeHTML(t, format) },
		"tokens":   compactTokens,
		"tokensOf": func(u syncapi.UsageTotals) string { return compactTokens(query.Tokens(u)) },
		"inputOf":  func(u syncapi.UsageTotals) string { return compactTokens(u.Input + u.CachedInput + u.CacheWrite) },
		"cachedOf": func(u syncapi.UsageTotals) float64 {
			if in := u.Input + u.CachedInput + u.CacheWrite; in > 0 {
				return float64(u.CachedInput) / float64(in) * 100
			}
			return 0
		},
		"usd":     usd,
		"width":   width,
		"svgX":    func(pct float64) string { return strconv.FormatFloat(pct*chartWidth/100, 'f', 1, 64) },
		"join":    strings.Join,
		"usageOf": func(u usageData, t map[string]string) usageArgs { return usageArgs{U: u, T: t} },
		"capacityOf": func(r syncapi.CapacityReport, t map[string]string) capacityArgs {
			return capacityArgs{R: r, T: t}
		},
		"card": func(a accountView, t map[string]string, admin bool) cardData {
			return cardData{A: a, T: t, Admin: admin}
		},
		"modelLabel": func(model, gateway string) struct{ Name, Via, Title string } {
			name, via, title := modelName(model, gateway)
			return struct{ Name, Via, Title string }{name, via, title}
		},
		"modes": func(c *chartView) map[string]chartMode {
			return map[string]chartMode{"amount": c.Amount, "cumulative": c.Cumulative}
		},
		"iso":      func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"timeText": func(format string, t time.Time) string { return t.Local().Format(timeFormats[format]) },

		// The capacity page's codes, tags and rows.
		"code":        codeText,
		"basisTag":    basisTag,
		"fitTag":      fitTag,
		"scenarioRow": newScenarioRow,
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "overview", "people", "accounts", "devices", "usage", "settings", "dashboard", "dashboardusage", "dashboardcapacity", "plans", "capacity"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/account.html", "templates/report.html", "templates/capacityreport.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		pages[name] = t
	}
	return pages, nil
}

// scenarioRow is one plan's, or one member's, row of the capacity tables.
type scenarioRow struct {
	T      map[string]string
	Name   string
	Member bool
	Five   syncapi.FiveHourResult
	Week   syncapi.WeeklyResult
}

func newScenarioRow(t map[string]string, name string, member bool, five syncapi.FiveHourResult, week syncapi.WeeklyResult) scenarioRow {
	return scenarioRow{T: t, Name: name, Member: member, Five: five, Week: week}
}

// basisTag and fitTag mark where a capacity came from and how a replay's
// peak reads.
func basisTag(t map[string]string, basis string) template.HTML {
	tone := map[string]string{string(quota.BasisDerived): " warn", string(quota.BasisInsufficient): " off"}[basis]
	return template.HTML(`<span class="tag` + tone + `">` + template.HTMLEscapeString(codeText(t, "basis", basis)) + `</span>`)
}

func fitTag(t map[string]string, fit string) template.HTML {
	tone := map[string]string{quota.FitBorderline: " warn", quota.FitOver: " danger"}[fit]
	return template.HTML(`<span class="tag` + tone + `">` + template.HTMLEscapeString(codeText(t, "fit", fit)) + `</span>`)
}

// cardData is what the shared account card receives; Admin adds what only
// the console shows, such as email hints and device names.
type cardData struct {
	A     accountView
	T     map[string]string
	Admin bool
}

// usageArgs is what the shared usage report templates receive.
type usageArgs struct {
	U usageData
	T map[string]string
}

// capacityArgs is what the shared capacity report template receives.
type capacityArgs struct {
	R syncapi.CapacityReport
	T map[string]string
}

// page is what every template receives. Title and Error hold dictionary
// keys; render translates them.
type page struct {
	Title  string
	Nav    string
	Error  string
	Data   any
	Authed bool
	// Public is a dashboard page, which anyone may see once published.
	Public bool

	Lang      string
	T         map[string]string
	Languages []struct{ ID, Label string }
	// Here is the GET path of the current page, for the language switch.
	Here string

	Version string
}

var navPaths = map[string]string{
	"overview": "/admin/", "usage": "/admin/usage", "people": "/admin/people", "accounts": "/admin/accounts",
	"devices": "/admin/devices", "settings": "/admin/settings", "dashboard": dashboardPath,
	"dashboardUsage": dashboardUsagePath, "dashboardCapacity": dashboardCapacityPath,
}

func (c *Console) render(w http.ResponseWriter, r *http.Request, name string, status int, p page) {
	p.Lang = requestLang(r)
	p.T = dictionaries[p.Lang]
	p.Languages = langNames
	p.Version = c.version
	p.Title = p.T[p.Title]
	if p.Error != "" {
		p.Error = p.T[p.Error]
	}
	p.Here = "/admin/login"
	if path, ok := navPaths[p.Nav]; ok {
		p.Here = path
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	w.WriteHeader(status)
	if err := c.pages[name].Execute(w, p); err != nil {
		c.log.Error("render admin page", "page", name, "err", err)
	}
}

func (c *Console) fail(w http.ResponseWriter, action string, err error) {
	c.log.Error(action, "err", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

// Sessions live in memory; restarting the server signs everyone out.

func (c *Console) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c.signedIn(r) {
			next(w, r)
			return
		}
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	}
}

// signedIn looks at every session cookie sent: one an earlier version kept
// under /admin can come along with the current one.
func (c *Console) signedIn(r *http.Request) bool {
	for _, cookie := range r.CookiesNamed(sessionCookie) {
		if c.validSession(cookie.Value) {
			return true
		}
	}
	return false
}

func (c *Console) validSession(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expires, ok := c.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(c.sessions, id)
		return false
	}
	return true
}

func (c *Console) loginPage(w http.ResponseWriter, r *http.Request) {
	c.render(w, r, "login", http.StatusOK, page{Title: "login"})
}

func (c *Console) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	given := sha256.Sum256([]byte(r.PostFormValue("password")))
	if subtle.ConstantTimeCompare(given[:], c.password[:]) != 1 {
		c.loginMu.Lock()
		time.Sleep(time.Second)
		c.loginMu.Unlock()
		c.log.Warn("admin login failed", "remote", r.RemoteAddr)
		c.render(w, r, "login", http.StatusUnauthorized, page{Title: "login", Error: "wrongPassword"})
		return
	}

	b := make([]byte, 32)
	rand.Read(b)
	id := hex.EncodeToString(b)
	now := time.Now()
	c.mu.Lock()
	for sid, exp := range c.sessions {
		if now.After(exp) {
			delete(c.sessions, sid)
		}
	}
	c.sessions[id] = now.Add(sessionTTL)
	c.mu.Unlock()

	// Site-wide, so an admin can preview the dashboard before publishing it.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/admin", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   strings.HasPrefix(c.publicURL, "https://"),
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (c *Console) logout(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	for _, cookie := range r.CookiesNamed(sessionCookie) {
		delete(c.sessions, cookie.Value)
	}
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/admin", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

type overviewData struct {
	Accounts []accountView
	// Day and Month are everyone's usage over the last 24 hours and 30
	// days.
	Day, Month syncapi.UsageTotals
}

func (c *Console) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	o, err := query.Overview(ctx, c.store, "", now, now)
	if err != nil {
		c.fail(w, "build overview", err)
		return
	}
	accounts, err := c.store.Accounts(ctx)
	if err != nil {
		c.fail(w, "list accounts", err)
		return
	}
	hints := map[string]string{}
	for _, a := range accounts {
		hints[a.ID] = a.Hint
	}
	data := overviewData{Accounts: accountViews(o, hints, now, dictionaries[requestLang(r)])}
	for _, p := range []struct {
		since time.Time
		into  *syncapi.UsageTotals
	}{{now.Add(-24 * time.Hour), &data.Day}, {now.Add(-30 * 24 * time.Hour), &data.Month}} {
		report, err := query.Usage(ctx, c.store, p.since)
		if err != nil {
			c.fail(w, "build usage report", err)
			return
		}
		*p.into = report.Total
	}
	c.render(w, r, "overview", http.StatusOK, page{Title: "navOverview", Nav: "overview", Authed: true, Data: data})
}

type personRow struct {
	identity.Person
	ActiveDevices int
	LastSeenAt    *time.Time
	// Month is their usage over the last 30 days.
	Month syncapi.UsageTotals
}

type inviteView struct {
	Person  string
	Link    string
	Expires time.Time
}

type peopleData struct {
	People []personRow
	Invite *inviteView
}

func (c *Console) peopleData(r *http.Request) (peopleData, error) {
	persons, err := c.store.Persons(r.Context())
	if err != nil {
		return peopleData{}, err
	}
	devices, err := c.store.Devices(r.Context())
	if err != nil {
		return peopleData{}, err
	}
	report, err := query.Usage(r.Context(), c.store, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		return peopleData{}, err
	}
	month := map[string]syncapi.UsageTotals{}
	for _, m := range report.Members {
		month[m.ID] = m.UsageTotals
	}
	active := map[string]int{}
	lastSeen := map[string]*time.Time{}
	for _, d := range devices {
		if d.RevokedAt == nil {
			active[d.PersonID]++
		}
		if d.LastSeenAt != nil && (lastSeen[d.PersonID] == nil || d.LastSeenAt.After(*lastSeen[d.PersonID])) {
			lastSeen[d.PersonID] = d.LastSeenAt
		}
	}
	var data peopleData
	for _, p := range persons {
		data.People = append(data.People, personRow{Person: p, ActiveDevices: active[p.ID], LastSeenAt: lastSeen[p.ID], Month: month[p.ID]})
	}
	return data, nil
}

func (c *Console) people(w http.ResponseWriter, r *http.Request) {
	data, err := c.peopleData(r)
	if err != nil {
		c.fail(w, "list people", err)
		return
	}
	c.render(w, r, "people", http.StatusOK, page{Title: "navPeople", Nav: "people", Authed: true, Data: data})
}

func (c *Console) addPerson(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	name := strings.TrimSpace(r.PostFormValue("name"))
	var formErr string
	switch {
	case name == "":
		formErr = "nameRequired"
	case len([]rune(name)) > 50:
		formErr = "nameTooLong"
	}
	if formErr == "" {
		_, err := c.store.AddPerson(r.Context(), name)
		if errors.Is(err, storage.ErrDuplicate) {
			formErr = "nameTaken"
		} else if err != nil {
			c.fail(w, "add person", err)
			return
		}
	}
	if formErr != "" {
		data, err := c.peopleData(r)
		if err != nil {
			c.fail(w, "list people", err)
			return
		}
		c.render(w, r, "people", http.StatusUnprocessableEntity, page{Title: "navPeople", Nav: "people", Authed: true, Error: formErr, Data: data})
		return
	}
	http.Redirect(w, r, "/admin/people", http.StatusSeeOther)
}

// invite shows the join link on the response itself: only its hash is
// stored, so it cannot be displayed again later.
func (c *Console) invite(w http.ResponseWriter, r *http.Request) {
	p, err := c.store.Person(r.Context(), r.PathValue("id"))
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.fail(w, "find person", err)
		return
	}
	code, expires, err := c.store.CreateInvite(r.Context(), p.ID, storage.InviteTTL)
	if err != nil {
		c.fail(w, "create invite", err)
		return
	}
	data, err := c.peopleData(r)
	if err != nil {
		c.fail(w, "list people", err)
		return
	}
	data.Invite = &inviteView{Person: p.DisplayName, Link: c.publicURL + syncapi.PathJoin + code, Expires: expires}
	c.render(w, r, "people", http.StatusOK, page{Title: "navPeople", Nav: "people", Authed: true, Data: data})
}

func (c *Console) deletePerson(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := c.store.DeletePerson(r.Context(), id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		c.fail(w, "delete person", err)
		return
	}
	c.log.Info("person deleted", "person", id)
	http.Redirect(w, r, "/admin/people", http.StatusSeeOther)
}

type memberRow struct {
	PersonID        string
	Name            string
	Weight          float64
	AllottedPercent float64
}

type accountRow struct {
	ID, Provider, Label, Hint, PlanType string
	Members                             []memberRow
	Others                              []identity.Person
}

func (c *Console) accounts(w http.ResponseWriter, r *http.Request) {
	c.renderAccounts(w, r, http.StatusOK, "")
}

func (c *Console) renderAccounts(w http.ResponseWriter, r *http.Request, status int, formErr string) {
	ctx := r.Context()
	accounts, err := c.store.Accounts(ctx)
	if err != nil {
		c.fail(w, "list accounts", err)
		return
	}
	persons, err := c.store.Persons(ctx)
	if err != nil {
		c.fail(w, "list people", err)
		return
	}
	var rows []accountRow
	for _, a := range accounts {
		members, err := c.store.Members(ctx, a.ID)
		if err != nil {
			c.fail(w, "list members", err)
			return
		}
		var total float64
		in := map[string]bool{}
		for _, m := range members {
			total += m.ShareWeight
			in[m.PersonID] = true
		}
		row := accountRow{ID: a.ID, Provider: string(a.Provider), Label: a.Label, Hint: a.Hint, PlanType: a.PlanType}
		for _, m := range members {
			mr := memberRow{PersonID: m.PersonID, Name: m.Name, Weight: m.ShareWeight}
			if total > 0 {
				mr.AllottedPercent = m.ShareWeight / total * 100
			}
			row.Members = append(row.Members, mr)
		}
		for _, p := range persons {
			if !in[p.ID] {
				row.Others = append(row.Others, p)
			}
		}
		rows = append(rows, row)
	}
	c.render(w, r, "accounts", status, page{Title: "navAccounts", Nav: "accounts", Authed: true, Error: formErr, Data: rows})
}

func (c *Console) setLabel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	label := strings.TrimSpace(r.PostFormValue("label"))
	if label == "" || len([]rune(label)) > 50 {
		c.renderAccounts(w, r, http.StatusUnprocessableEntity, "labelLength")
		return
	}
	a, err := c.store.Account(r.Context(), r.PathValue("id"))
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.fail(w, "find account", err)
		return
	}
	if err := c.store.SetAccountLabel(r.Context(), a.ID, label); err != nil {
		c.fail(w, "set account label", err)
		return
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

// setShares saves every weight in the form. Fields are named
// "weight.<personID>"; "add_person" with "add_weight" adds a member.
func (c *Console) setShares(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Malformed form", http.StatusBadRequest)
		return
	}
	a, err := c.store.Account(r.Context(), r.PathValue("id"))
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.fail(w, "find account", err)
		return
	}

	weights := map[string]float64{}
	for key, vals := range r.PostForm {
		personID, ok := strings.CutPrefix(key, "weight.")
		if !ok || len(vals) == 0 {
			continue
		}
		v, ok := parseWeight(vals[0])
		if !ok {
			c.renderAccounts(w, r, http.StatusUnprocessableEntity, "weightRange")
			return
		}
		weights[personID] = v
	}
	if add := r.PostFormValue("add_person"); add != "" {
		v, ok := parseWeight(r.PostFormValue("add_weight"))
		if !ok {
			c.renderAccounts(w, r, http.StatusUnprocessableEntity, "weightRange")
			return
		}
		if _, err := c.store.Person(r.Context(), add); err != nil {
			c.renderAccounts(w, r, http.StatusUnprocessableEntity, "personNotFound")
			return
		}
		weights[add] = v
	}

	ids := make([]string, 0, len(weights))
	for id := range weights {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := c.store.SetShareWeight(r.Context(), a.ID, id, weights[id]); err != nil {
			c.fail(w, "set share weight", err)
			return
		}
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

// setAccountOrder saves the order accounts were dragged into, listed as
// repeated "id" fields; IDs of accounts that no longer exist are dropped.
func (c *Console) setAccountOrder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Malformed form", http.StatusBadRequest)
		return
	}
	accounts, err := c.store.Accounts(r.Context())
	if err != nil {
		c.fail(w, "list accounts", err)
		return
	}
	var ids []string
	for _, id := range r.PostForm["id"] {
		if !slices.Contains(ids, id) && slices.ContainsFunc(accounts, func(a account.Account) bool { return a.ID == id }) {
			ids = append(ids, id)
		}
	}
	if err := c.store.SetAccountOrder(r.Context(), ids); err != nil {
		c.fail(w, "set account order", err)
		return
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

func (c *Console) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := c.store.DeleteAccount(r.Context(), id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		c.fail(w, "delete account", err)
		return
	}
	c.log.Info("account deleted", "account", id)
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

// plansData is the plan history page: the account's intervals, the plans a
// Claude account can be given, and the form as last submitted so an error
// leaves it filled in.
type plansData struct {
	Account account.Account
	Plans   []account.PlanInterval
	// Choices are the plans to pick from; empty for a provider whose plans
	// are not known, which type one in.
	Choices []string
	Form    planForm
}

type planForm struct {
	Plan, Precision, Reason string
	// EffectiveLocal and EndedLocal are the browser's own date and time
	// fields, in its time zone.
	EffectiveLocal, EndedLocal string
}

func (c *Console) accountPlans(w http.ResponseWriter, r *http.Request) {
	c.renderPlans(w, r, http.StatusOK, "", planForm{Precision: account.PrecisionConfirmed})
}

func (c *Console) renderPlans(w http.ResponseWriter, r *http.Request, status int, formErr string, form planForm) {
	ctx := r.Context()
	accID := r.PathValue("id")
	acc, err := c.store.Account(ctx, accID)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.fail(w, "find account", err)
		return
	}
	plans, err := c.store.PlanHistory(ctx, accID)
	if err != nil {
		c.fail(w, "list plan history", err)
		return
	}
	data := plansData{Account: acc, Plans: plans, Form: form}
	if acc.Provider == account.ProviderAnthropic {
		data.Choices = quota.KnownPlans()
	}
	c.render(w, r, "plans", status, page{
		Title: "planHistory", Nav: "accounts", Authed: true, Error: formErr, Data: data,
	})
}

// saveAccountPlan saves a plan interval. The form's times are RFC 3339,
// which the page's script writes from the date and time the admin entered
// in the browser's time zone; the server's own zone says nothing about it.
func (c *Console) saveAccountPlan(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Malformed form", http.StatusBadRequest)
		return
	}
	accID := r.PathValue("id")
	if _, err := c.store.Account(r.Context(), accID); errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		c.fail(w, "find account", err)
		return
	}
	form := planForm{
		Plan:           strings.TrimSpace(r.PostFormValue("plan_type")),
		Precision:      strings.TrimSpace(r.PostFormValue("precision")),
		Reason:         strings.TrimSpace(r.PostFormValue("reason")),
		EffectiveLocal: r.PostFormValue("effective_local"),
		EndedLocal:     r.PostFormValue("ended_local"),
	}
	if form.Precision == "" {
		form.Precision = account.PrecisionConfirmed
	}
	invalid := func(key string) { c.renderPlans(w, r, http.StatusUnprocessableEntity, key, form) }

	if form.Plan == "" {
		invalid("planRequired")
		return
	}
	eff, err := time.Parse(time.RFC3339, strings.TrimSpace(r.PostFormValue("effective_at")))
	if err != nil {
		invalid("planTimeInvalid")
		return
	}
	var endedAt *time.Time
	if endRaw := strings.TrimSpace(r.PostFormValue("ended_at")); endRaw != "" {
		end, err := time.Parse(time.RFC3339, endRaw)
		if err != nil {
			invalid("planTimeInvalid")
			return
		}
		endedAt = &end
	}

	in := account.PlanInterval{
		ID:          strings.TrimSpace(r.PostFormValue("interval_id")),
		AccountID:   accID,
		PlanType:    form.Plan,
		EffectiveAt: eff,
		EndedAt:     endedAt,
		Reason:      form.Reason,
		Source:      "admin",
		Precision:   form.Precision,
	}
	switch _, err := c.store.SavePlanInterval(r.Context(), in); {
	case errors.Is(err, storage.ErrPlanIntervalOverlap):
		invalid("planOverlap")
		return
	case errors.Is(err, storage.ErrInvalidPlanInterval):
		invalid("planEndInvalid")
		return
	case errors.Is(err, storage.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		c.fail(w, "save plan interval", err)
		return
	}
	http.Redirect(w, r, "/admin/accounts/"+accID+"/plans", http.StatusSeeOther)
}

func (c *Console) deleteAccountPlan(w http.ResponseWriter, r *http.Request) {
	accID := r.PathValue("id")
	intervalID := r.PathValue("pid")
	if err := c.store.DeletePlanInterval(r.Context(), accID, intervalID); err != nil {
		c.log.Warn("delete plan interval failed", "err", err)
	}
	http.Redirect(w, r, "/admin/accounts/"+accID+"/plans", http.StatusSeeOther)
}

func (c *Console) accountCapacity(w http.ResponseWriter, r *http.Request) {
	accID := r.PathValue("id")
	period := query.PeriodByID(r.URL.Query().Get(syncapi.QueryPeriod))
	rep, err := query.AccountCapacity(r.Context(), c.store, accID, period, time.Now())
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.fail(w, "build capacity report", err)
		return
	}
	c.render(w, r, "capacity", http.StatusOK, page{
		Title: "capacity", Nav: "accounts", Authed: true,
		Data: rep,
	})
}

func parseWeight(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > 100 {
		return 0, false
	}
	return v, true
}

type deviceRow struct {
	storage.DeviceRow
	Online bool
	// SignedIn names the accounts the device is signed into now.
	SignedIn []string
	Month    syncapi.UsageTotals
}

func (c *Console) devices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	devices, err := c.store.Devices(ctx)
	if err != nil {
		c.fail(w, "list devices", err)
		return
	}
	accounts, err := c.store.Accounts(ctx)
	if err != nil {
		c.fail(w, "list accounts", err)
		return
	}
	active, err := c.store.ActiveDevices(ctx, now.Add(-query.ActiveWindow))
	if err != nil {
		c.fail(w, "list active devices", err)
		return
	}
	report, err := query.Usage(ctx, c.store, now.Add(-30*24*time.Hour))
	if err != nil {
		c.fail(w, "build usage report", err)
		return
	}
	names := map[string]string{}
	for _, a := range accounts {
		names[a.ID] = providerName(string(a.Provider)) + " · " + a.Label
	}
	signedIn := map[string][]string{}
	for _, a := range active {
		signedIn[a.DeviceID] = append(signedIn[a.DeviceID], names[a.AccountID])
	}
	rows := make([]deviceRow, 0, len(devices))
	for _, d := range devices {
		in := signedIn[d.ID]
		// A device on one account in both the CLI and Claude Desktop
		// names it once.
		sort.Strings(in)
		rows = append(rows, deviceRow{
			DeviceRow: d,
			Online:    d.RevokedAt == nil && d.LastSeenAt != nil && now.Sub(*d.LastSeenAt) <= query.ActiveWindow,
			SignedIn:  slices.Compact(in),
			Month:     report.Devices[d.ID],
		})
	}
	c.render(w, r, "devices", http.StatusOK, page{Title: "navDevices", Nav: "devices", Authed: true, Data: rows})
}

// periodOf is the period a request asks for, 30 days by default.
func periodOf(r *http.Request) query.Period {
	return query.PeriodByID(r.URL.Query().Get(syncapi.QueryPeriod))
}

// usageData is a usage report and its period switch, which links to Path.
type usageData struct {
	Path    string
	Period  string
	Periods []query.Period
	Report  query.UsageReport
	// Member is set when the report is one member's.
	Member *identity.Person
	// ShowDevices lists a member's devices by name, which only the console
	// may.
	ShowDevices bool
}

// requestedMember is the member a usage page asks for with ?member=, nil
// when it asks for everyone. ok is false once it has answered the request.
func (c *Console) requestedMember(w http.ResponseWriter, r *http.Request) (member *identity.Person, ok bool) {
	id := r.URL.Query().Get("member")
	if id == "" {
		return nil, true
	}
	p, err := c.store.Person(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		c.fail(w, "find member", err)
		return nil, false
	}
	return &p, true
}

// usageReport is the usage report of a member, or of everyone when member is
// nil.
func (c *Console) usageReport(ctx context.Context, member *identity.Person, since time.Time) (query.UsageReport, error) {
	if member != nil {
		return query.UsageBy(ctx, c.store, since, member.ID)
	}
	return query.Usage(ctx, c.store, since)
}

func (c *Console) usage(w http.ResponseWriter, r *http.Request) {
	member, ok := c.requestedMember(w, r)
	if !ok {
		return
	}
	period := periodOf(r)
	report, err := c.usageReport(r.Context(), member, time.Now().Add(-period.Span()))
	if err != nil {
		c.fail(w, "build usage report", err)
		return
	}
	data := usageData{Path: "/admin/usage", Period: period.ID, Periods: query.Periods, Report: report, Member: member, ShowDevices: true}
	c.render(w, r, "usage", http.StatusOK, page{Title: "navUsage", Nav: "usage", Authed: true, Data: data})
}

func (c *Console) revoke(w http.ResponseWriter, r *http.Request) {
	err := c.store.RevokeDevice(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		c.fail(w, "revoke device", err)
		return
	}
	http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
}

// percent rounds a share to a whole percent, showing a small non-zero one
// as <1%.
func percent(v float64) string {
	if v > 0 && v < 1 {
		return "<1%"
	}
	return strconv.Itoa(int(math.Round(v))) + "%"
}

// compactTokens renders a token count as 950, 12.3K, 4.5M.
func compactTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + "B"
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func providerName(p string) string {
	switch p {
	case "anthropic":
		return "Claude"
	case "openai":
		return "Codex"
	case "google":
		return "Antigravity"
	}
	return p
}

// Times are rendered in the server's time zone and carry their instant, so
// the page's script can show them in the viewer's; the formats match.
var timeFormats = map[string]string{
	"datetime": "2006-01-02 15:04",
	"clock":    "15:04",
	"day":      "1/2",
	"dayclock": "1/2 15:04",
}

func timeHTML(t any, format string) template.HTML {
	var v time.Time
	switch tv := t.(type) {
	case time.Time:
		v = tv
	case *time.Time:
		if tv == nil {
			return "—"
		}
		v = *tv
	default:
		return ""
	}
	return template.HTML(fmt.Sprintf(`<time datetime="%s" data-dt="%[1]s" data-f="%s">%s</time>`,
		v.UTC().Format(time.RFC3339), format, v.Local().Format(timeFormats[format])))
}

// codeText translates a code a report carries, such as "unknown_plan" under
// "excluded", from the dictionary key "excludedUnknownPlan"; "model-specific"
// under "limitKind" is "limitKindModelSpecific". An unknown code shows as it
// is.
func codeText(t map[string]string, prefix, code string) string {
	key := prefix
	for _, part := range strings.FieldsFunc(code, func(r rune) bool { return r == '_' || r == '-' }) {
		key += strings.ToUpper(part[:1]) + part[1:]
	}
	if text, ok := t[key]; ok {
		return text
	}
	return code
}

// tfHTML fills a translated format with arguments that may be markup, such
// as a time.
func tfHTML(format string, args ...any) template.HTML {
	escaped := make([]any, len(args))
	for i, a := range args {
		if h, ok := a.(template.HTML); ok {
			escaped[i] = h
		} else {
			escaped[i] = template.HTMLEscapeString(fmt.Sprint(a))
		}
	}
	return template.HTML(fmt.Sprintf(template.HTMLEscapeString(format), escaped...))
}

// width is a bar length for a style attribute, kept within 0 to 100%.
func width(v float64) string {
	return strconv.FormatFloat(max(0, min(100, v)), 'f', 2, 64) + "%"
}

// usd is an estimated price in US dollars; cents only below $100.
func usd(v float64) string {
	digits := 0
	if v < 100 {
		digits = 2
	}
	s := strconv.FormatFloat(v, 'f', digits, 64)
	whole, frac, _ := strings.Cut(s, ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if frac != "" {
		whole += "." + frac
	}
	return "US$" + whole
}
