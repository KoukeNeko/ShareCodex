// Package query builds the read models the desktop popup shows.
package query

import (
	"context"
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

// Overview returns every account with its quota windows and, per window,
// each member's allotment and estimated usage. All members see all accounts.
func Overview(ctx context.Context, st *storage.Store, viewerPersonID string, now time.Time) (syncapi.Overview, error) {
	accounts, err := st.Accounts(ctx)
	if err != nil {
		return syncapi.Overview{}, err
	}
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
			Buckets: []syncapi.BucketOverview{},
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
	return out, nil
}

func bucketOverview(b quota.Observed, now time.Time, members []storage.Member, rows []storage.UsageRow, viewer string) syncapi.BucketOverview {
	bo := syncapi.BucketOverview{Key: string(b.Key), ResetsAt: b.ResetsAt, ObservedAt: b.ObservedAt, Members: []syncapi.MemberShare{}}
	if b.WindowMinutes != nil {
		bo.WindowMinutes = *b.WindowMinutes
	}
	if b.UsedPercent != nil {
		bo.UsedPercent = *b.UsedPercent
	}

	costs := map[string]float64{}
	requests := map[string]int{}
	if b.HasReset(now) {
		// The window closed after the last report; nothing is used yet.
		bo.Reset = true
		bo.UsedPercent = 0
	} else {
		for _, r := range rows {
			costs[r.PersonID] += attribution.Cost(r.Model, r.Tokens)
			requests[r.PersonID] += r.Requests
		}
	}

	weights := map[string]float64{}
	names := map[string]string{}
	for _, m := range members {
		weights[m.PersonID] = m.ShareWeight
		names[m.PersonID] = m.Name
	}

	shares, unattributed := attribution.Apportion(bo.UsedPercent, costs, weights)
	bo.UnattributedPercent = unattributed
	for person, s := range shares {
		bo.Members = append(bo.Members, syncapi.MemberShare{
			PersonID:        person,
			Name:            names[person],
			IsYou:           person == viewer,
			ShareWeight:     weights[person],
			AllottedPercent: s.AllottedPercent,
			UsedPercent:     s.UsedPercent,
			Requests:        requests[person],
		})
	}
	sort.Slice(bo.Members, func(i, j int) bool {
		if bo.Members[i].UsedPercent != bo.Members[j].UsedPercent {
			return bo.Members[i].UsedPercent > bo.Members[j].UsedPercent
		}
		return bo.Members[i].Name < bo.Members[j].Name
	})
	return bo
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
