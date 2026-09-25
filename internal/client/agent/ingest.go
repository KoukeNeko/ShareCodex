package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/desktop"
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
	{account.ProviderAnthropic, claudeRoots, func(f *os.File) (parsed, error) {
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

// claudeRoots adds the transcripts of Claude Desktop's Cowork sessions to
// Claude Code's own.
func claudeRoots() []string {
	roots := transcript.Roots()
	if dir, err := desktop.Dir(); err == nil {
		roots = append(roots, desktop.CoworkRoots(dir)...)
	}
	return roots
}

// accounts attributes one provider's usage to pooled accounts.
type accounts struct {
	provider account.Provider
	// cli is the CLI's sign-in timeline.
	cli []account.Observation
	// desktopSeen is true once Claude Desktop has been observed.
	desktopSeen bool
	// desktop holds Claude Desktop's sessions by ID, which Desktop runs under
	// its own sign-in; see loadDesktopSessions.
	desktop map[string]desktop.Session
	// since is the device's first observation from any app. Usage from
	// before it is not part of the shared ledger.
	since time.Time
}

func (a *Agent) loadAccounts(ctx context.Context, p account.Provider) (accounts, error) {
	cli, err := a.store.Observations(ctx, p, account.SourceCLI)
	if err != nil {
		return accounts{}, err
	}
	ac := accounts{provider: p, cli: cli}
	if len(cli) > 0 {
		ac.since = cli[0].ObservedAt
	}
	if p != account.ProviderAnthropic {
		return ac, nil
	}
	dsk, err := a.store.Observations(ctx, p, account.SourceClaudeDesktop)
	if err != nil {
		return accounts{}, err
	}
	if len(dsk) > 0 {
		ac.desktopSeen = true
		if ac.since.IsZero() || dsk[0].ObservedAt.Before(ac.since) {
			ac.since = dsk[0].ObservedAt
		}
	}
	return ac, nil
}

// loadDesktopSessions reads Claude Desktop's sessions, which resolve needs
// for Desktop usage. It is separate from loadAccounts so a scan that finds
// no changed files does not read them.
func (ac *accounts) loadDesktopSessions() error {
	if !ac.desktopSeen {
		return nil
	}
	dir, err := desktop.Dir()
	if err != nil {
		return err
	}
	if ac.desktop, err = desktop.Sessions(dir); err != nil {
		return fmt.Errorf("read Claude Desktop sessions: %w", err)
	}
	return nil
}

// pooled reports whether any usage can be attributed yet: the CLI has been
// seen on a pooled account, or Claude Desktop has been used.
func (ac accounts) pooled() bool {
	return anyPooled(ac.cli) || ac.desktopSeen
}

// resolve returns the account of usage at t in a session. A Desktop session
// uses the organization Desktop ran it under; any other session uses the
// CLI's account at t. Desktop sessions whose metadata is gone, and Desktop
// set up for third-party inference, have no known pooled account.
func (ac accounts) resolve(sessionID, entrypoint string, t time.Time) (string, bool) {
	if ac.provider == account.ProviderAnthropic {
		if s, ok := ac.desktop[sessionID]; ok {
			if t.Before(ac.since) {
				return "", false
			}
			return account.HashExternalRef(account.ProviderAnthropic, s.OrgID), true
		}
		if desktop.FromDesktop(entrypoint) {
			return "", false
		}
	}
	ref, ok := account.ResolveAt(ac.cli, ac.provider, t)
	return ref, ok && ref != ""
}

// scanSource ingests changed files of one provider. Nothing is read until
// the device has been seen signed into a pooled account: usage from before
// joining is not part of the shared ledger.
func (a *Agent) scanSource(ctx context.Context, src source, known map[string]scan.FileState) (int, error) {
	ac, err := a.loadAccounts(ctx, src.provider)
	if err != nil {
		return 0, err
	}
	if !ac.pooled() {
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
		if _, seen := known[p]; !seen && time.Since(lastObservedAt(ac.cli)) > time.Minute {
			a.observe(ctx, src.provider)
			if ac, err = a.loadAccounts(ctx, src.provider); err != nil {
				return 0, err
			}
			break
		}
	}
	if err := ac.loadDesktopSessions(); err != nil {
		return 0, err
	}

	total := 0
	for _, path := range paths {
		n, err := a.ingestFile(ctx, src, path, current[path], ac)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (a *Agent) ingestFile(ctx context.Context, src source, path string, st scan.FileState, ac accounts) (int, error) {
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
		if ref, ok := ac.resolve(e.SessionID, e.Originator, e.OccurredAt); ok {
			e.AccountRefHash = ref
			events = append(events, e)
		}
	}
	// Rollout snapshots carry no session; they follow the CLI's timeline.
	var snaps []quota.Snapshot
	for _, snap := range res.snapshots {
		if ref, ok := ac.resolve("", "", snap.ObservedAt); ok {
			snap.AccountRefHash = ref
			snaps = append(snaps, snap)
		}
	}
	return a.store.IngestFile(ctx, path, st, events, snaps)
}

func (a *Agent) ingestStatusLine(ctx context.Context) (int, error) {
	dir := a.SpoolDir()
	if err := statusline.PruneSpool(dir, spoolMaxAge, time.Now()); err != nil {
		return 0, err
	}
	spooled, err := statusline.ReadSpool(dir)
	if err != nil || len(spooled) == 0 {
		return 0, err
	}
	ac, err := a.loadAccounts(ctx, account.ProviderAnthropic)
	if err != nil {
		return 0, err
	}
	if err := ac.loadDesktopSessions(); err != nil {
		return 0, err
	}
	n := 0
	for _, sp := range spooled {
		// Claude Desktop's sessions run the statusLine too, under Desktop's
		// own account.
		ref, ok := ac.resolve(sp.SessionID, "", sp.Snapshot.ObservedAt)
		if !ok {
			continue
		}
		sp.Snapshot.AccountRefHash = ref
		if err := a.store.AddSnapshot(ctx, sp.Snapshot); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func anyPooled(obs []account.Observation) bool {
	for _, o := range obs {
		if o.ExternalRefHash != "" {
			return true
		}
	}
	return false
}
