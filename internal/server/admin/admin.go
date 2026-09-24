// Package admin is the server's web console: people and join links,
// accounts and quota shares, and devices. It replaces the former admin CLI
// so a deployment needs no shell access to the container.
package admin

import (
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/identity"
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
	inviteTTL     = 24 * time.Hour
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
}

type Console struct {
	store     *storage.Store
	log       *slog.Logger
	password  [32]byte
	publicURL string
	pages     map[string]*template.Template

	mu       sync.Mutex
	sessions map[string]time.Time
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
		pages:     pages,
		sessions:  map[string]time.Time{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/lang/{lang}", c.setLang)
	mux.HandleFunc("GET /admin/login", c.loginPage)
	mux.HandleFunc("POST /admin/login", c.login)
	mux.HandleFunc("POST /admin/logout", c.logout)
	mux.HandleFunc("GET /admin/{$}", c.authed(c.overview))
	mux.HandleFunc("GET /admin/people", c.authed(c.people))
	mux.HandleFunc("POST /admin/people", c.authed(c.addPerson))
	mux.HandleFunc("POST /admin/people/{id}/invite", c.authed(c.invite))
	mux.HandleFunc("GET /admin/accounts", c.authed(c.accounts))
	mux.HandleFunc("POST /admin/accounts/{id}/label", c.authed(c.setLabel))
	mux.HandleFunc("POST /admin/accounts/{id}/shares", c.authed(c.setShares))
	mux.HandleFunc("GET /admin/devices", c.authed(c.devices))
	mux.HandleFunc("POST /admin/devices/{id}/revoke", c.authed(c.revoke))

	// Forms post with the session cookie; reject cross-site requests.
	return http.NewCrossOriginProtection().Handler(mux), nil
}

func parsePages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"pct":      func(v float64) string { return strconv.Itoa(int(math.Round(v))) + "%" },
		"weight":   func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) },
		"provider": providerName,
		"bucket":   bucketName,
		"tf":       fmt.Sprintf,
		"when":     formatTime,
		"over":     func(m syncapi.MemberShare) bool { return m.UsedPercent > m.AllottedPercent+0.5 },
		"tokens":   compactTokens,
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "overview", "people", "accounts", "devices"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		pages[name] = t
	}
	return pages, nil
}

// page is what every template receives. Title and Error hold dictionary
// keys; render translates them.
type page struct {
	Title  string
	Nav    string
	Error  string
	Data   any
	Authed bool

	Lang      string
	T         map[string]string
	Languages []struct{ ID, Label string }
	// Here is the GET path of the current page, for the language switch.
	Here string
}

var navPaths = map[string]string{
	"overview": "/admin/", "people": "/admin/people", "accounts": "/admin/accounts", "devices": "/admin/devices",
}

func (c *Console) render(w http.ResponseWriter, r *http.Request, name string, status int, p page) {
	p.Lang = requestLang(r)
	p.T = dictionaries[p.Lang]
	p.Languages = langNames
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
		cookie, err := r.Cookie(sessionCookie)
		if err == nil && c.validSession(cookie.Value) {
			next(w, r)
			return
		}
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	}
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

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/admin",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   strings.HasPrefix(c.publicURL, "https://"),
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (c *Console) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		c.mu.Lock()
		delete(c.sessions, cookie.Value)
		c.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/admin", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (c *Console) overview(w http.ResponseWriter, r *http.Request) {
	o, err := query.Overview(r.Context(), c.store, "", time.Now())
	if err != nil {
		c.fail(w, "build overview", err)
		return
	}
	c.render(w, r, "overview", http.StatusOK, page{Title: "navOverview", Nav: "overview", Authed: true, Data: o})
}

type personRow struct {
	identity.Person
	ActiveDevices int
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
	active := map[string]int{}
	for _, d := range devices {
		if d.RevokedAt == nil {
			active[d.PersonID]++
		}
	}
	var data peopleData
	for _, p := range persons {
		data.People = append(data.People, personRow{Person: p, ActiveDevices: active[p.ID]})
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
	code, expires, err := c.store.CreateInvite(r.Context(), p.ID, inviteTTL)
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

func parseWeight(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > 100 {
		return 0, false
	}
	return v, true
}

func (c *Console) devices(w http.ResponseWriter, r *http.Request) {
	devices, err := c.store.Devices(r.Context())
	if err != nil {
		c.fail(w, "list devices", err)
		return
	}
	c.render(w, r, "devices", http.StatusOK, page{Title: "navDevices", Nav: "devices", Authed: true, Data: devices})
}

func (c *Console) revoke(w http.ResponseWriter, r *http.Request) {
	err := c.store.RevokeDevice(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		c.fail(w, "revoke device", err)
		return
	}
	http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
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
	}
	return p
}

func bucketName(key string, windowMinutes int, t map[string]string) string {
	switch key {
	case "five_hour":
		return t["fiveHour"]
	case "weekly":
		return t["weekly"]
	}
	if windowMinutes%1440 == 0 && windowMinutes > 0 {
		return fmt.Sprintf(t["days"], windowMinutes/1440)
	}
	if windowMinutes%60 == 0 && windowMinutes > 0 {
		return fmt.Sprintf(t["hours"], windowMinutes/60)
	}
	return fmt.Sprintf(t["minutes"], windowMinutes)
}

func formatTime(t any) string {
	switch v := t.(type) {
	case time.Time:
		return v.Local().Format("2006-01-02 15:04")
	case *time.Time:
		if v == nil {
			return "—"
		}
		return v.Local().Format("2006-01-02 15:04")
	}
	return ""
}
