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
// each member's allotment and estimated usage. All members see all accounts.
func Overview(ctx context.Context, st *storage.Store, viewerPersonID string, now time.Time) (syncapi.Overview, error) {
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
			ao.Buckets = append(ao.Buckets, bucketOverview(b, now, members, rows, viewerPersonID))
		}
		out.Accounts = append(out.Accounts, ao)
	}
	// The accounts the viewer is signed into come first.
	sort.SliceStable(out.Accounts, func(i, j int) bool {
		return usedByViewer(out.Accounts[i]) && !usedByViewer(out.Accounts[j])
	})
	return out, nil
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
	var totalCost float64
	if b.HasReset(now) {
		// The window closed after the last report; nothing is used yet.
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
		}
		// Same order as bo.Models so the popup can color segments by model.
		for _, m := range bo.Models {
			if c := personModelCosts[person][m.Model]; c > 0 && totalCost > 0 {
				ms.Models = append(ms.Models, syncapi.MemberModel{Model: m.Model, UsedPercent: bo.UsedPercent * c / totalCost})
			}
		}
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
