// Package query builds the read models the desktop popup shows.
package query

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/attribution"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// snapshotLookback covers the longest window (weekly) plus slack, so the
// latest value of every bucket is found.
const snapshotLookback = 8 * 24 * time.Hour

// activeWindow is how recent a device's last observation must be for its
// account to count as in use. Devices report every 5 minutes, so this
// tolerates two missed reports.
const activeWindow = 15 * time.Minute

// Overview returns every account with its quota windows and, per window,
// each member's allotment and estimated usage, plus the viewer's own usage
// since today (the start of the viewer's day) and over 30 days. Members see
// every account except those they have left.
func Overview(ctx context.Context, st *storage.Store, viewerPersonID string, now, today time.Time) (syncapi.Overview, error) {
	accounts, err := st.Accounts(ctx)
	if err != nil {
		return syncapi.Overview{}, err
	}
	active, err := st.ActiveDevices(ctx, now.Add(-activeWindow))
	if err != nil {
		return syncapi.Overview{}, err
	}
	activeUsers := groupActiveUsers(active, viewerPersonID)
	out := syncapi.Overview{GeneratedAt: now.UTC(), Accounts: []syncapi.AccountOverview{}}
	for _, a := range accounts {
		snaps, err := st.Snapshots(ctx, a.ID, now.Add(-snapshotLookback))
		if err != nil {
			return syncapi.Overview{}, err
		}
		members, err := st.Members(ctx, a.ID)
		if err != nil {
			return syncapi.Overview{}, err
		}

		// A member who left an account, or whom an admin set to weight 0,
		// no longer sees it.
		if slices.ContainsFunc(members, func(m storage.Member) bool {
			return m.PersonID == viewerPersonID && m.ShareWeight == 0
		}) {
			continue
		}

		ao := syncapi.AccountOverview{
			ID: a.ID, Provider: string(a.Provider), Label: a.Label, PlanType: a.PlanType,
			Buckets: []syncapi.BucketOverview{}, ActiveUsers: activeUsers[a.ID],
		}
		if ao.ActiveUsers == nil {
			ao.ActiveUsers = []syncapi.ActiveUser{}
		}
		for _, b := range sortedBuckets(quota.Latest(snaps)) {
			start, end := b.Window(now)
			rows, err := st.Usage(ctx, a.ID, start, end)
			if err != nil {
				return syncapi.Overview{}, err
			}
			bo := bucketOverview(b, now, members, rows, viewerPersonID)
			if bo.Timeline, err = timeline(ctx, st, a.ID, b, snaps, now); err != nil {
				return syncapi.Overview{}, err
			}
			ao.Buckets = append(ao.Buckets, bo)
		}
		out.Accounts = append(out.Accounts, ao)
	}
	// The admin console views as no one.
	if viewerPersonID != "" {
		you := syncapi.PersonalUsage{}
		for _, p := range []struct {
			since time.Time
			into  *syncapi.UsageTotals
		}{{today, &you.Today}, {now.Add(-30 * 24 * time.Hour), &you.Last30Day}} {
			rows, err := st.PersonUsage(ctx, viewerPersonID, p.since)
			if err != nil {
				return syncapi.Overview{}, err
			}
			*p.into = usageTotals(rows)
		}
		out.You = &you
	}

	// The accounts the viewer is signed into come first.
	sort.SliceStable(out.Accounts, func(i, j int) bool {
		return usedByViewer(out.Accounts[i]) && !usedByViewer(out.Accounts[j])
	})
	return out, nil
}

func usageTotals(rows []storage.PersonUsage) syncapi.UsageTotals {
	var u syncapi.UsageTotals
	for _, r := range rows {
		u.Input += r.Tokens.Input
		u.CachedInput += r.Tokens.CachedInput
		u.CacheWrite += r.Tokens.CacheWrite
		u.Output += r.Tokens.Output
		u.Requests += r.Requests
		u.CostUSD += attribution.Cost(r.Model, r.Tokens)
	}
	return u
}

// timelineBinMinutes keeps a chart near 20–30 points: 15 minutes for a
// 5-hour window, 6 hours for a week.
func timelineBinMinutes(windowMinutes int) int {
	switch {
	case windowMinutes <= 6*60:
		return 15
	case windowMinutes <= 24*60:
		return 60
	default:
		return 6 * 60
	}
}

// resetStarted is how far into a window a reading must be for its reset
// to count: a reading of a window that has not started reports its reset a
// full window away.
const resetStarted = 2 * time.Minute

// pastResets lists the reset times readings of one bucket reported within
// (start, end]. A reading of a window that has not started (Codex reports
// one a full window after every idle reading) is no reset, so a time counts
// only once a reading shows its window running. Times closer than merge are
// one reset, the later kept: readings of one window differ slightly in their
// reset times, and a window starts about when the last one reset.
func pastResets(snaps []quota.Snapshot, key quota.BucketKey, start, end time.Time, merge time.Duration) []time.Time {
	var times []time.Time
	for _, s := range snaps {
		for _, b := range s.Buckets {
			if b.Key != key || b.ResetsAt == nil || b.WindowMinutes == nil {
				continue
			}
			window := time.Duration(*b.WindowMinutes) * time.Minute
			if b.ResetsAt.Sub(s.ObservedAt) > window-resetStarted {
				continue
			}
			if at := b.ResetsAt.UTC().Round(time.Minute); at.After(start) && !at.After(end) {
				times = append(times, at)
			}
		}
	}
	slices.SortFunc(times, time.Time.Compare)
	out := []time.Time{}
	for _, at := range times {
		if n := len(out); n > 0 && at.Sub(out[n-1]) < merge {
			out[n-1] = at
			continue
		}
		out = append(out, at)
	}
	return out
}

// timeline covers the last window length before now rather than the current
// window, so the previous window's usage stays in view after a reset.
func timeline(ctx context.Context, st *storage.Store, accountID string, b quota.Observed, snaps []quota.Snapshot, now time.Time) (*syncapi.Timeline, error) {
	if b.WindowMinutes == nil || *b.WindowMinutes <= 0 {
		return nil, nil
	}
	window := *b.WindowMinutes
	bin := timelineBinMinutes(window)
	bins := (window + bin - 1) / bin
	start := now.Add(-time.Duration(bins*bin) * time.Minute)
	rows, err := st.UsageTimeline(ctx, accountID, start, now, bin)
	if err != nil {
		return nil, err
	}
	merge := time.Duration(bin) * time.Minute
	resets := pastResets(snaps, b.Key, start, now, merge)
	// The current window's start is a reset too, though only its end is
	// ever reported; like an earlier one, it counts once a reading shows the
	// window running.
	if b.ResetsAt != nil && b.ResetsAt.Sub(b.ObservedAt) <= time.Duration(window)*time.Minute-resetStarted {
		at := b.ResetsAt.Add(-time.Duration(window) * time.Minute).UTC().Round(time.Minute)
		if at.After(start) &&
			!slices.ContainsFunc(resets, func(r time.Time) bool { return r.Sub(at).Abs() < merge }) {
			resets = append(resets, at)
			slices.SortFunc(resets, time.Time.Compare)
		}
	}
	tl := &syncapi.Timeline{Start: start.UTC(), BinMinutes: bin, Bins: bins, Points: []syncapi.TimelinePoint{}, Resets: resets}
	for _, r := range rows {
		tl.Points = append(tl.Points, syncapi.TimelinePoint{Bin: r.Bin, PersonID: r.PersonID, Model: r.Model, Tokens: r.Tokens})
	}
	return tl, nil
}

func usedByViewer(a syncapi.AccountOverview) bool {
	return slices.ContainsFunc(a.ActiveUsers, func(u syncapi.ActiveUser) bool { return u.IsYou })
}

func bucketOverview(b quota.Observed, now time.Time, members []storage.Member, rows []storage.UsageRow, viewer string) syncapi.BucketOverview {
	bo := syncapi.BucketOverview{Key: string(b.Key), ResetsAt: b.ResetsAt, ObservedAt: b.ObservedAt,
		Members: []syncapi.MemberShare{}, Models: []syncapi.ModelUsage{}}
	if b.WindowMinutes != nil {
		bo.WindowMinutes = *b.WindowMinutes
	}
	if b.UsedPercent != nil {
		bo.UsedPercent = *b.UsedPercent
	}

	costs := map[string]float64{}
	requests := map[string]int{}
	models := map[string]*syncapi.ModelUsage{}
	modelCosts := map[string]float64{}
	personModelCosts := map[string]map[string]float64{}
	personDeviceCosts := map[string]map[string]float64{}
	var totalCost float64
	if b.HasReset(now) {
		// No new provider reading after reset: the current percentage is unknown.
		bo.Reset = true
		bo.UsedPercent = 0
	} else {
		for _, r := range rows {
			cost := attribution.Cost(r.Model, r.Tokens)
			costs[r.PersonID] += cost
			requests[r.PersonID] += r.Requests
			totalCost += cost

			m, ok := models[r.Model]
			if !ok {
				m = &syncapi.ModelUsage{Model: r.Model}
				models[r.Model] = m
			}
			m.Requests += r.Requests
			m.Tokens += r.Tokens.Input + r.Tokens.CachedInput + r.Tokens.CacheWrite + r.Tokens.Output
			modelCosts[r.Model] += cost
			if personModelCosts[r.PersonID] == nil {
				personModelCosts[r.PersonID] = map[string]float64{}
			}
			personModelCosts[r.PersonID][r.Model] += cost
			if personDeviceCosts[r.PersonID] == nil {
				personDeviceCosts[r.PersonID] = map[string]float64{}
			}
			personDeviceCosts[r.PersonID][r.DeviceName] += cost
		}
	}
	for name, m := range models {
		if totalCost > 0 {
			m.UsedPercent = bo.UsedPercent * modelCosts[name] / totalCost
		}
		bo.Models = append(bo.Models, *m)
	}
	sort.Slice(bo.Models, func(i, j int) bool {
		if bo.Models[i].UsedPercent != bo.Models[j].UsedPercent {
			return bo.Models[i].UsedPercent > bo.Models[j].UsedPercent
		}
		return bo.Models[i].Model < bo.Models[j].Model
	})

	weights := map[string]float64{}
	names := map[string]string{}
	for _, m := range members {
		weights[m.PersonID] = m.ShareWeight
		names[m.PersonID] = m.Name
	}

	shares, unattributed := attribution.Apportion(bo.UsedPercent, costs, weights)
	bo.UnattributedPercent = unattributed
	for person, s := range shares {
		ms := syncapi.MemberShare{
			PersonID:        person,
			Name:            names[person],
			IsYou:           person == viewer,
			ShareWeight:     weights[person],
			AllottedPercent: s.AllottedPercent,
			UsedPercent:     s.UsedPercent,
			Requests:        requests[person],
			Models:          []syncapi.MemberModel{},
			Devices:         []syncapi.MemberDevice{},
		}
		// Same order as bo.Models so the popup can color segments by model.
		for _, m := range bo.Models {
			if c := personModelCosts[person][m.Model]; c > 0 && totalCost > 0 {
				ms.Models = append(ms.Models, syncapi.MemberModel{Model: m.Model, UsedPercent: bo.UsedPercent * c / totalCost})
			}
		}
		for name, c := range personDeviceCosts[person] {
			if c > 0 && totalCost > 0 {
				ms.Devices = append(ms.Devices, syncapi.MemberDevice{Name: name, UsedPercent: bo.UsedPercent * c / totalCost})
			}
		}
		sort.Slice(ms.Devices, func(i, j int) bool {
			if ms.Devices[i].UsedPercent != ms.Devices[j].UsedPercent {
				return ms.Devices[i].UsedPercent > ms.Devices[j].UsedPercent
			}
			return ms.Devices[i].Name < ms.Devices[j].Name
		})
		bo.Members = append(bo.Members, ms)
	}
	sort.Slice(bo.Members, func(i, j int) bool {
		if bo.Members[i].UsedPercent != bo.Members[j].UsedPercent {
			return bo.Members[i].UsedPercent > bo.Members[j].UsedPercent
		}
		return bo.Members[i].Name < bo.Members[j].Name
	})
	return bo
}

// groupActiveUsers turns active devices into each account's people, one
// entry per person with their device names, the viewer first and then by
// name.
func groupActiveUsers(devices []storage.ActiveDevice, viewer string) map[string][]syncapi.ActiveUser {
	byAccount := map[string][]syncapi.ActiveUser{}
	for _, d := range devices {
		users := byAccount[d.AccountID]
		i := slices.IndexFunc(users, func(u syncapi.ActiveUser) bool { return u.PersonID == d.PersonID })
		if i < 0 {
			users = append(users, syncapi.ActiveUser{PersonID: d.PersonID, Name: d.PersonName, IsYou: d.PersonID == viewer})
			i = len(users) - 1
		}
		users[i].Devices = append(users[i].Devices, d.DeviceName)
		byAccount[d.AccountID] = users
	}
	for _, users := range byAccount {
		// A device on one account in both the CLI and Claude Desktop is
		// listed once.
		for i := range users {
			sort.Strings(users[i].Devices)
			users[i].Devices = slices.Compact(users[i].Devices)
		}
		sort.Slice(users, func(i, j int) bool {
			if users[i].IsYou != users[j].IsYou {
				return users[i].IsYou
			}
			return users[i].Name < users[j].Name
		})
	}
	return byAccount
}

var bucketOrder = map[quota.BucketKey]int{quota.BucketFiveHour: 0, quota.BucketWeekly: 1}

// sortedBuckets puts the known windows first (5h, then weekly), then any
// others by key.
func sortedBuckets(latest map[quota.BucketKey]quota.Observed) []quota.Observed {
	out := make([]quota.Observed, 0, len(latest))
	for _, b := range latest {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, iKnown := bucketOrder[out[i].Key]
		oj, jKnown := bucketOrder[out[j].Key]
		if iKnown != jKnown {
			return iKnown
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].Key < out[j].Key
	})
	return out
}
