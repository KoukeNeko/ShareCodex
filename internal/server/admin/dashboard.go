package admin

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/server/query"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// The public dashboard shows the overview without sign-in once an admin
// publishes it: each account's windows, members' names and estimated usage,
// and models. Email hints and device names stay in the console.

const dashboardPath = "/dashboard"

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

type dashboardData struct {
	Accounts    []accountView
	GeneratedAt time.Time
	// Preview is an admin viewing the dashboard before it is published.
	Preview bool
	Usage   usageData
}

func (c *Console) dashboard(w http.ResponseWriter, r *http.Request) {
	on, err := c.store.PublicDashboard(r.Context())
	if err != nil {
		c.fail(w, "read dashboard setting", err)
		return
	}
	// Admins preview it under /admin, where their session cookie is sent.
	preview := r.URL.Path != dashboardPath
	if !on && !preview {
		http.NotFound(w, r)
		return
	}
	o, err := c.dash.overview.get(r.Context(), "", c.publicOverview)
	if err != nil {
		c.fail(w, "build dashboard", err)
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

	t := dictionaries[requestLang(r)]
	// Accounts still named by their email are numbered, the same in the
	// cards and the usage report.
	names := map[string]string{}
	n := 0
	for _, a := range o.Accounts {
		names[a.ID] = a.Label
		if a.Label == "" {
			n++
			names[a.ID] = fmt.Sprintf(t["accountN"], n)
		}
	}
	data := dashboardData{
		Accounts:    accountViews(o, nil, time.Now(), t),
		GeneratedAt: o.GeneratedAt,
		Preview:     !on,
	}
	for i, a := range o.Accounts {
		data.Accounts[i].Label = names[a.ID]
	}
	// The cached report is shared, so its account names change on a copy.
	report.Accounts = slices.Clone(report.Accounts)
	for i, a := range report.Accounts {
		report.Accounts[i].Name = names[a.ID]
	}
	data.Usage = usageData{Path: r.URL.Path, Period: period.ID, Periods: usagePeriods, Report: report}
	nav := "dashboard"
	if preview {
		nav = "preview"
	}
	c.render(w, r, "dashboard", http.StatusOK, page{Title: "dashboard", Nav: nav, Data: data})
}

// publicOverview is the overview without what the dashboard does not
// publish: an account still named by its masked email is left unnamed, to
// be numbered instead, as the desktop app's shared image does.
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
		if a.Label == hints[a.ID] {
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
