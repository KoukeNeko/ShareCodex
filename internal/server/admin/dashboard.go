package admin

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/server/query"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// The public dashboard shows the overview without sign-in once an admin
// publishes it: each account's windows, members' names and estimated usage,
// and models. Email hints and device names stay in the console.

const (
	dashboardPath      = "/dashboard"
	dashboardUsagePath = "/dashboard/usage"
)

// dashboardTTL bounds how often anyone opening the dashboard can make the
// server rebuild the overview.
const dashboardTTL = 30 * time.Second

type dashboardCache struct {
	overview ttlCache[syncapi.Overview]
	// usage holds a report per period ID.
	usage ttlCache[query.UsageReport]
}

// ttlCache keeps each value for dashboardTTL after building it.
type ttlCache[T any] struct {
	mu      sync.Mutex
	entries map[string]ttlEntry[T]
}

type ttlEntry[T any] struct {
	at    time.Time
	value T
}

func (c *ttlCache[T]) get(ctx context.Context, key string, build func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && time.Since(e.at) < dashboardTTL {
		return e.value, nil
	}
	v, err := build(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	if c.entries == nil {
		c.entries = map[string]ttlEntry[T]{}
	}
	c.entries[key] = ttlEntry[T]{at: time.Now(), value: v}
	return v, nil
}

//go:embed static/*.png
var staticFS embed.FS

// manifest lets a phone install the public dashboard as an app of its own.
func (c *Console) manifest(w http.ResponseWriter, r *http.Request) {
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose,omitempty"`
	}
	m := struct {
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		StartURL        string `json:"start_url"`
		Scope           string `json:"scope"`
		Display         string `json:"display"`
		BackgroundColor string `json:"background_color"`
		ThemeColor      string `json:"theme_color"`
		Icons           []icon `json:"icons"`
	}{
		Name: "ShareCodex", ShortName: "ShareCodex", StartURL: dashboardPath, Scope: "/", Display: "standalone",
		// The dark theme's background, the one installed apps open on.
		BackgroundColor: "#0c0c0d", ThemeColor: "#0c0c0d",
		Icons: []icon{
			{Src: "/icons/icon-192.png", Sizes: "192x192", Type: "image/png"},
			{Src: "/icons/icon-512.png", Sizes: "512x512", Type: "image/png"},
			{Src: "/icons/icon-maskable-512.png", Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if err := json.NewEncoder(w).Encode(m); err != nil {
		c.log.Error("write manifest", "err", err)
	}
}

func (c *Console) icon(w http.ResponseWriter, r *http.Request) {
	b, err := staticFS.ReadFile("static/" + r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(b)
}

// root sends visitors to the dashboard when it is published, else to the
// console.
func (c *Console) root(w http.ResponseWriter, r *http.Request) {
	on, err := c.store.PublicDashboard(r.Context())
	if err != nil {
		c.fail(w, "read dashboard setting", err)
		return
	}
	if on {
		http.Redirect(w, r, dashboardPath, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// Admins see the dashboard before it is published, with a notice.
type publicData struct {
	GeneratedAt time.Time
	Preview     bool
}

type dashboardData struct {
	publicData
	Accounts []accountView
}

type dashboardUsageData struct {
	publicData
	Usage usageData
}

// publicView gathers what both dashboard pages need: whether the visitor
// may see them, the cached overview, and the names accounts are published
// under. ok is false once it has answered the request.
func (c *Console) publicView(w http.ResponseWriter, r *http.Request) (o syncapi.Overview, names map[string]string, pd publicData, ok bool) {
	on, err := c.store.PublicDashboard(r.Context())
	if err != nil {
		c.fail(w, "read dashboard setting", err)
		return o, nil, pd, false
	}
	if !on && !c.signedIn(r) {
		http.NotFound(w, r)
		return o, nil, pd, false
	}
	o, err = c.dash.overview.get(r.Context(), "", c.publicOverview)
	if err != nil {
		c.fail(w, "build dashboard", err)
		return o, nil, pd, false
	}
	// Accounts still named by their email are numbered, the same on both
	// pages.
	t := dictionaries[requestLang(r)]
	names = map[string]string{}
	n := 0
	for _, a := range o.Accounts {
		names[a.ID] = a.Label
		if a.Label == "" {
			n++
			names[a.ID] = fmt.Sprintf(t["accountN"], n)
		}
	}
	return o, names, publicData{GeneratedAt: o.GeneratedAt, Preview: !on}, true
}

func (c *Console) dashboard(w http.ResponseWriter, r *http.Request) {
	o, names, pd, ok := c.publicView(w, r)
	if !ok {
		return
	}
	data := dashboardData{publicData: pd, Accounts: accountViews(o, nil, time.Now(), dictionaries[requestLang(r)])}
	for i, a := range o.Accounts {
		data.Accounts[i].Label = names[a.ID]
	}
	c.render(w, r, "dashboard", http.StatusOK, page{Title: "dashboard", Nav: "dashboard", Public: true, Data: data})
}

func (c *Console) dashboardUsage(w http.ResponseWriter, r *http.Request) {
	_, names, pd, ok := c.publicView(w, r)
	if !ok {
		return
	}
	period := periodOf(r)
	report, err := c.dash.usage.get(r.Context(), period.ID, func(ctx context.Context) (query.UsageReport, error) {
		return query.Usage(ctx, c.store, time.Now().Add(-period.span()))
	})
	if err != nil {
		c.fail(w, "build dashboard usage", err)
		return
	}
	// The cached report is shared, so its account names change on a copy.
	report.Accounts = slices.Clone(report.Accounts)
	for i, a := range report.Accounts {
		report.Accounts[i].Name = names[a.ID]
	}
	data := dashboardUsageData{publicData: pd, Usage: usageData{Path: dashboardUsagePath, Period: period.ID, Periods: usagePeriods, Report: report}}
	c.render(w, r, "dashboardusage", http.StatusOK, page{Title: "navUsage", Nav: "dashboardUsage", Public: true, Data: data})
}

// publicOverview is the overview without what the dashboard does not
// publish: an account still named by its masked email is left unnamed, to
// be numbered instead, as the desktop app's shared image does. A label that
// is no longer the account's hint, because a later reading reported another
// one, is still a masked email and stays unpublished.
func (c *Console) publicOverview(ctx context.Context) (syncapi.Overview, error) {
	now := time.Now()
	o, err := query.Overview(ctx, c.store, "", now, now)
	if err != nil {
		return syncapi.Overview{}, err
	}
	accounts, err := c.store.Accounts(ctx)
	if err != nil {
		return syncapi.Overview{}, err
	}
	hints := map[string]string{}
	for _, a := range accounts {
		hints[a.ID] = a.Hint
	}
	for i, a := range o.Accounts {
		if a.Label == hints[a.ID] || account.IsMasked(a.Label) {
			o.Accounts[i].Label = ""
		}
	}
	return o, nil
}

type settingsData struct {
	PublicDashboard bool
	DashboardURL    string
}

func (c *Console) settings(w http.ResponseWriter, r *http.Request) {
	on, err := c.store.PublicDashboard(r.Context())
	if err != nil {
		c.fail(w, "read dashboard setting", err)
		return
	}
	data := settingsData{PublicDashboard: on, DashboardURL: c.publicURL + dashboardPath}
	c.render(w, r, "settings", http.StatusOK, page{Title: "navSettings", Nav: "settings", Authed: true, Data: data})
}

func (c *Console) saveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	on := r.PostFormValue("public_dashboard") == "on"
	if err := c.store.SetPublicDashboard(r.Context(), on); err != nil {
		c.fail(w, "save dashboard setting", err)
		return
	}
	c.log.Info("public dashboard set", "published", on)
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
