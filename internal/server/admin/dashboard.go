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
	dashboardPath         = "/dashboard"
	dashboardUsagePath    = "/dashboard/usage"
	dashboardCapacityPath = "/dashboard/capacity"
)

// dashboardTTL bounds how often anyone opening the dashboard can make the
// server rebuild the overview.
const dashboardTTL = 30 * time.Second

type dashboardCache struct {
	overview ttlCache[syncapi.Overview]
	// usage holds a report per period ID, and per member and period.
	usage ttlCache[query.UsageReport]
	// capacity holds a report per account and period ID.
	capacity ttlCache[syncapi.CapacityReport]
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
	member, ok := c.requestedMember(w, r)
	if !ok {
		return
	}
	period := periodOf(r)
	// A member's report is cached apart from everyone's; the member exists,
	// so the cache holds at most a report per member and period.
	key := period.ID
	if member != nil {
		key += "/" + member.ID
	}
	report, err := c.dash.usage.get(r.Context(), key, func(ctx context.Context) (query.UsageReport, error) {
		return c.usageReport(ctx, member, time.Now().Add(-period.Span()))
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
	data := dashboardUsageData{publicData: pd, Usage: usageData{Path: dashboardUsagePath, Period: period.ID, Periods: query.Periods, Report: report, Member: member}}
	c.render(w, r, "dashboardusage", http.StatusOK, page{Title: "navUsage", Nav: "dashboardUsage", Public: true, Data: data})
}

type dashboardCapacityData struct {
	publicData
	Path    string
	Period  string
	Periods []query.Period
	// Accounts are the public overview's, under their public names;
	// AccountID is the one the report is of, empty when there is none to show.
	Accounts  []publicAccount
	AccountID string
	Report    syncapi.CapacityReport
}

type publicAccount struct{ ID, Name string }

func (c *Console) dashboardCapacity(w http.ResponseWriter, r *http.Request) {
	o, names, pd, ok := c.publicView(w, r)
	if !ok {
		return
	}
	now := time.Now()
	// An unreadable range shows the preset, with the reason.
	span, err := query.RangeFromQuery(r.URL.Query(), now)
	var notice string
	if err != nil {
		span, notice = query.PresetRange(periodOf(r), now), "rangeInvalid"
	}
	data := dashboardCapacityData{publicData: pd, Path: dashboardCapacityPath, Period: span.Period, Periods: query.Periods}
	for _, a := range o.Accounts {
		data.Accounts = append(data.Accounts, publicAccount{ID: a.ID, Name: names[a.ID]})
	}
	// Anthropic is the one provider with capacity scenarios, so it is the
	// default.
	accID := r.URL.Query().Get("account")
	if accID == "" {
		for _, a := range o.Accounts {
			if a.Provider == string(account.ProviderAnthropic) {
				accID = a.ID
				break
			}
		}
	} else if !slices.ContainsFunc(o.Accounts, func(a syncapi.AccountOverview) bool { return a.ID == accID }) {
		http.NotFound(w, r)
		return
	}
	if accID != "" {
		data.AccountID = accID
		build := func(ctx context.Context) (syncapi.CapacityReport, error) {
			return query.AccountCapacity(ctx, c.store, accID, span, now)
		}
		// Only the presets are cached, so the cache stays at a report per
		// account and period; a custom range, which anyone can vary without
		// end, is built each time, to the minute.
		var report syncapi.CapacityReport
		var err error
		if span.Period == query.PeriodCustom {
			span.Start, span.End = span.Start.Truncate(time.Minute), span.End.Truncate(time.Minute)
			report, err = build(r.Context())
		} else {
			report, err = c.dash.capacity.get(r.Context(), accID+"/"+span.Period, build)
		}
		if err != nil {
			c.fail(w, "build dashboard capacity", err)
			return
		}
		data.Report = publicCapacity(report, names[accID])
	}
	c.render(w, r, "dashboardcapacity", http.StatusOK, page{Title: "capacity", Nav: "dashboardCapacity", Public: true, Error: notice, Data: data})
}

// publicCapacity is a capacity report as the dashboard publishes it. The
// cached report is shared, so what changes is on a copy: the account is
// named as the dashboard names it, and the plan intervals lose the admin's
// reason and source.
func publicCapacity(report syncapi.CapacityReport, name string) syncapi.CapacityReport {
	report.AccountLabel = name
	report.Plans = slices.Clone(report.Plans)
	for i := range report.Plans {
		report.Plans[i].Reason, report.Plans[i].Source = "", ""
	}
	return report
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
