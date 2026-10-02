package query

import (
	"context"
	"sort"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/attribution"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// Period is a span a usage report covers.
type Period struct {
	ID          string
	Hours, Days int
}

func (p Period) Span() time.Duration {
	return time.Duration(p.Hours)*time.Hour + time.Duration(p.Days)*24*time.Hour
}

// Periods are the spans a usage report offers.
var Periods = []Period{{"24h", 24, 0}, {"7d", 0, 7}, {"30d", 0, 30}}

// PeriodByID is the period named id, 30 days when id names none.
func PeriodByID(id string) Period {
	for _, p := range Periods {
		if p.ID == id {
			return p
		}
	}
	return Periods[len(Periods)-1]
}

// UsageReport is everyone's usage since a time, for the admin console.
// Totals, members, accounts and devices count only the quota's own models;
// third-party models, which never count against a quota, are listed apart.
type UsageReport struct {
	Total      syncapi.UsageTotals
	Members    []UsageLine
	Accounts   []UsageLine
	Models     []ModelLine
	ThirdParty []ModelLine
	// Devices holds each device's totals by device ID.
	Devices map[string]syncapi.UsageTotals
	// DeviceLines are the devices that sent requests, most costly first, with
	// their names; UsageBy sets them for a member's report only.
	DeviceLines []UsageLine
}

// UsageLine is the totals of one member or account, most costly first.
// Provider is set for an account.
type UsageLine struct {
	ID       string
	Name     string
	Provider string
	syncapi.UsageTotals
}

type ModelLine struct {
	Model   string
	Gateway string
	syncapi.UsageTotals
}

// Usage is everyone's usage since a time.
func Usage(ctx context.Context, st *storage.Store, since time.Time) (UsageReport, error) {
	return usageSince(ctx, st, since, "")
}

// UsageBy is one member's usage since a time, with their devices named.
func UsageBy(ctx context.Context, st *storage.Store, since time.Time, personID string) (UsageReport, error) {
	r, err := usageSince(ctx, st, since, personID)
	if err != nil {
		return UsageReport{}, err
	}
	devices, err := st.Devices(ctx)
	if err != nil {
		return UsageReport{}, err
	}
	lines := map[string]*UsageLine{}
	for _, d := range devices {
		if t, ok := r.Devices[d.ID]; ok {
			lines[d.ID] = &UsageLine{ID: d.ID, Name: d.Name, UsageTotals: t}
		}
	}
	r.DeviceLines = sortedLines(lines)
	return r, nil
}

// MemberUsage is one member's usage over a period as the client API serves
// it; viewerID is the person asking. A member who does not exist is
// storage.ErrNotFound.
func MemberUsage(ctx context.Context, st *storage.Store, viewerID, personID string, period Period, now time.Time) (syncapi.MemberUsage, error) {
	p, err := st.Person(ctx, personID)
	if err != nil {
		return syncapi.MemberUsage{}, err
	}
	r, err := UsageBy(ctx, st, now.Add(-period.Span()), personID)
	if err != nil {
		return syncapi.MemberUsage{}, err
	}
	out := syncapi.MemberUsage{
		PersonID: p.ID, Name: p.DisplayName, IsYou: p.ID == viewerID, Period: period.ID, Total: r.Total,
		Accounts:   make([]syncapi.AccountUsage, 0, len(r.Accounts)),
		Models:     make([]syncapi.ModelTotals, 0, len(r.Models)),
		ThirdParty: make([]syncapi.ModelTotals, 0, len(r.ThirdParty)),
		Devices:    make([]syncapi.DeviceUsage, 0, len(r.DeviceLines)),
	}
	for _, a := range r.Accounts {
		out.Accounts = append(out.Accounts, syncapi.AccountUsage{ID: a.ID, Provider: a.Provider, Label: a.Name, Totals: a.UsageTotals})
	}
	for _, m := range r.Models {
		out.Models = append(out.Models, syncapi.ModelTotals{Model: m.Model, Gateway: m.Gateway, Totals: m.UsageTotals})
	}
	for _, m := range r.ThirdParty {
		out.ThirdParty = append(out.ThirdParty, syncapi.ModelTotals{Model: m.Model, Gateway: m.Gateway, Totals: m.UsageTotals})
	}
	for _, d := range r.DeviceLines {
		out.Devices = append(out.Devices, syncapi.DeviceUsage{Name: d.Name, Totals: d.UsageTotals})
	}
	return out, nil
}

func usageSince(ctx context.Context, st *storage.Store, since time.Time, personID string) (UsageReport, error) {
	rows, err := st.UsageSummary(ctx, since, personID)
	if err != nil {
		return UsageReport{}, err
	}
	accounts, err := st.Accounts(ctx)
	if err != nil {
		return UsageReport{}, err
	}

	r := UsageReport{Devices: map[string]syncapi.UsageTotals{}}
	members := map[string]*UsageLine{}
	byAccount := map[string]*UsageLine{}
	models := map[string]*ModelLine{}
	thirdParty := map[string]*ModelLine{}
	for _, row := range rows {
		cost := attribution.Cost(row.Model, row.Tokens)
		key := modelKey(row.Model, row.Gateway)
		if row.ThirdParty {
			// The price table weighs the quota's own models; another
			// vendor's model is shown by its tokens alone.
			addTotals(&line(thirdParty, key, row.Model, row.Gateway).UsageTotals, row, 0)
			continue
		}
		addTotals(&r.Total, row, cost)
		addTotals(&named(members, row.PersonID, row.PersonName).UsageTotals, row, cost)
		addTotals(&named(byAccount, row.AccountID, "").UsageTotals, row, cost)
		addTotals(&line(models, key, row.Model, row.Gateway).UsageTotals, row, cost)
		d := r.Devices[row.DeviceID]
		addTotals(&d, row, cost)
		r.Devices[row.DeviceID] = d
	}
	for _, a := range accounts {
		if l := byAccount[a.ID]; l != nil {
			l.Name, l.Provider = a.Label, string(a.Provider)
		}
	}
	r.Members = sortedLines(members)
	r.Accounts = sortedLines(byAccount)
	r.Models = sortedModels(models)
	r.ThirdParty = sortedModels(thirdParty)
	return r, nil
}

func addTotals(u *syncapi.UsageTotals, row storage.SummaryRow, cost float64) {
	u.Input += row.Tokens.Input
	u.CachedInput += row.Tokens.CachedInput
	u.CacheWrite += row.Tokens.CacheWrite
	u.Output += row.Tokens.Output
	u.Requests += row.Requests
	u.CostUSD += cost
}

func named(m map[string]*UsageLine, id, name string) *UsageLine {
	if m[id] == nil {
		m[id] = &UsageLine{ID: id, Name: name}
	}
	return m[id]
}

func line(m map[string]*ModelLine, key, model, gateway string) *ModelLine {
	if m[key] == nil {
		m[key] = &ModelLine{Model: model, Gateway: gateway}
	}
	return m[key]
}

// Tokens counts every token a request used, as the overview's models do.
func Tokens(u syncapi.UsageTotals) int64 {
	return u.Input + u.CachedInput + u.CacheWrite + u.Output
}

func sortedLines(m map[string]*UsageLine) []UsageLine {
	out := make([]UsageLine, 0, len(m))
	for _, l := range m {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func sortedModels(m map[string]*ModelLine) []ModelLine {
	out := make([]ModelLine, 0, len(m))
	for _, l := range m {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if ti, tj := Tokens(out[i].UsageTotals), Tokens(out[j].UsageTotals); ti != tj {
			return ti > tj
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Gateway < out[j].Gateway
	})
	return out
}
