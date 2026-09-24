package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/statusline"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/transcript"
	"github.com/KoukeNeko/ShareCodex/internal/provider/openai/codex/rollout"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/scan"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

type parsed struct {
	events    []usage.Event
	snapshots []quota.Snapshot
	badLines  int
}

type source struct {
	provider account.Provider
	roots    func() []string
	parse    func(*os.File) (parsed, error)
}

var sources = []source{
	{account.ProviderAnthropic, transcript.Roots, func(f *os.File) (parsed, error) {
		r, err := transcript.Parse(f)
		return parsed{events: r.Events, badLines: r.BadLines}, err
	}},
	{account.ProviderOpenAI, rollout.Roots, func(f *os.File) (parsed, error) {
		r, err := rollout.Parse(f)
		return parsed{events: r.Events, snapshots: r.Snapshots, badLines: r.BadLines}, err
	}},
}

func (a *Agent) scanAll(ctx context.Context) {
	known, err := a.store.KnownFiles(ctx)
	if err != nil {
		a.log.Error("load scanned files", "err", err)
		return
	}
	changed := false
	for _, src := range sources {
		n, err := a.scanSource(ctx, src, known)
		if err != nil {
			a.recordError(src.provider, err)
			continue
		}
		changed = changed || n > 0
	}
	n, err := a.ingestStatusLine(ctx)
	if err != nil {
		a.recordError(account.ProviderAnthropic, err)
	}
	if changed || n > 0 {
		kick(a.kickSync)
		a.changed()
	}
}

// scanSource ingests changed files of one provider. Nothing is read until
// the device has been seen signed into a pooled account: usage from before
// joining is not part of the shared ledger.
func (a *Agent) scanSource(ctx context.Context, src source, known map[string]scan.FileState) (int, error) {
	obs, err := a.store.Observations(ctx, src.provider)
	if err != nil {
		return 0, err
	}
	if !anyPooled(obs) {
		return 0, nil
	}
	current, err := scan.List(src.roots())
	if err != nil {
		return 0, fmt.Errorf("list session logs: %w", err)
	}
	paths := scan.Changed(known, current)
	if len(paths) == 0 {
		return 0, nil
	}

	// A new session may have started after an account switch; check the
	// identity before attributing it, at most once a minute.
	for _, p := range paths {
		if _, seen := known[p]; !seen && time.Since(lastObservedAt(obs)) > time.Minute {
			a.observe(ctx, src.provider)
			if obs, err = a.store.Observations(ctx, src.provider); err != nil {
				return 0, err
			}
			break
		}
	}

	total := 0
	for _, path := range paths {
		n, err := a.ingestFile(ctx, src, path, current[path], obs)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (a *Agent) ingestFile(ctx context.Context, src source, path string, st scan.FileState, obs []account.Observation) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	res, err := src.parse(f)
	f.Close()
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	if res.badLines > 0 {
		a.log.Warn("skipped unreadable lines", "file", path, "count", res.badLines)
	}

	events := res.events[:0]
	for _, e := range res.events {
		if ref, ok := account.ResolveAt(obs, src.provider, e.OccurredAt); ok && ref != "" {
			e.AccountRefHash = ref
			events = append(events, e)
		}
	}
	snaps := resolveSnapshots(res.snapshots, obs, src.provider)
	return a.store.IngestFile(ctx, path, st, events, snaps)
}

func (a *Agent) ingestStatusLine(ctx context.Context) (int, error) {
	dir := a.SpoolDir()
	if err := statusline.PruneSpool(dir, spoolMaxAge, time.Now()); err != nil {
		return 0, err
	}
	snaps, err := statusline.ReadSpool(dir)
	if err != nil || len(snaps) == 0 {
		return 0, err
	}
	obs, err := a.store.Observations(ctx, account.ProviderAnthropic)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range resolveSnapshots(snaps, obs, account.ProviderAnthropic) {
		if err := a.store.AddSnapshot(ctx, s); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func resolveSnapshots(snaps []quota.Snapshot, obs []account.Observation, p account.Provider) []quota.Snapshot {
	var out []quota.Snapshot
	for _, s := range snaps {
		if ref, ok := account.ResolveAt(obs, p, s.ObservedAt); ok && ref != "" {
			s.AccountRefHash = ref
			out = append(out, s)
		}
	}
	return out
}

func anyPooled(obs []account.Observation) bool {
	for _, o := range obs {
		if o.ExternalRefHash != "" {
			return true
		}
	}
	return false
}
