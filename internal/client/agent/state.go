package agent

import (
	"context"
	"os"
	"sort"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/config"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// State returns a consistent copy of what the popup shows.
func (a *Agent) State(ctx context.Context) State {
	a.mu.Lock()
	st := State{
		Version:       a.version,
		Paired:        a.settings.Paired(),
		Revoked:       a.revoked,
		PersonName:    a.settings.PersonName,
		DeviceName:    a.settings.DeviceName,
		ServerURL:     a.settings.ServerURL,
		LastSyncAt:    a.lastSync,
		SyncError:     a.syncErr,
		Overview:      a.overview,
		LaunchAtLogin: a.settings.LaunchAtLogin,
	}
	for _, p := range []account.Provider{account.ProviderAnthropic, account.ProviderOpenAI} {
		st.Providers = append(st.Providers, *a.providers[p])
	}
	a.mu.Unlock()

	if n, err := a.store.OutboxLen(ctx); err == nil {
		st.PendingUploads = n
	}
	st.Local = a.localAccounts(ctx, time.Now())
	st.StatusLineInstalled = statusLineInstalled()
	return st
}

func statusLineInstalled() bool {
	path, err := config.SettingsPath()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	plan, err := config.PlanRestore(b, nil)
	return err == nil && !plan.Empty()
}

// localAccounts summarises this device's own quota readings per account.
func (a *Agent) localAccounts(ctx context.Context, now time.Time) []LocalAccount {
	snaps, err := a.store.LatestSnapshots(ctx, now.Add(-8*24*time.Hour))
	if err != nil {
		a.log.Error("load local snapshots", "err", err)
		return nil
	}
	byAccount := map[string][]quota.Snapshot{}
	for _, s := range snaps {
		key := string(s.Provider) + ":" + s.AccountRefHash
		byAccount[key] = append(byAccount[key], s)
	}

	hints := map[string]account.Observation{}
	for _, p := range []account.Provider{account.ProviderAnthropic, account.ProviderOpenAI} {
		obs, err := a.store.Observations(ctx, p)
		if err != nil {
			continue
		}
		for _, o := range obs {
			hints[string(p)+":"+o.ExternalRefHash] = o
		}
	}

	var out []LocalAccount
	for key, group := range byAccount {
		o := hints[key]
		la := LocalAccount{Provider: group[0].Provider, Hint: o.Hint, PlanType: o.PlanType}
		for _, b := range quota.Latest(group) {
			bo := syncapi.BucketOverview{Key: string(b.Key), ResetsAt: b.ResetsAt, ObservedAt: b.ObservedAt}
			if b.UsedPercent != nil {
				bo.UsedPercent = *b.UsedPercent
			}
			if b.WindowMinutes != nil {
				bo.WindowMinutes = *b.WindowMinutes
			}
			if b.HasReset(now) {
				bo.Reset, bo.UsedPercent = true, 0
			}
			la.Buckets = append(la.Buckets, bo)
		}
		sort.Slice(la.Buckets, func(i, j int) bool { return la.Buckets[i].WindowMinutes < la.Buckets[j].WindowMinutes })
		out = append(out, la)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}
