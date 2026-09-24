// Package agent runs the desktop client's background work: watching the
// Claude Code and Codex logs, tracking which account each is signed into,
// and syncing the local ledger to the server. It coordinates the adapters
// and storage but holds no provider-specific parsing itself.
package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	gosync "sync"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/client/secret"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/client/storage"
	"github.com/KoukeNeko/ShareCodex/internal/client/sync"
	"github.com/KoukeNeko/ShareCodex/internal/client/update"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

const (
	scanInterval     = 15 * time.Second
	identityInterval = 5 * time.Minute
	syncInterval     = 30 * time.Second
	maxSyncBackoff   = 10 * time.Minute
	batchSize        = 500
	spoolMaxAge      = 7 * 24 * time.Hour
	updateInterval   = 24 * time.Hour
)

type Status string

const (
	StatusOK           Status = "ok"
	StatusNotInstalled Status = "not_installed"
	// StatusNotPooled means the CLI is signed out or uses an API key, so
	// its usage does not count against a shared subscription.
	StatusNotPooled Status = "not_pooled"
	StatusError     Status = "error"
)

type ProviderState struct {
	Provider    account.Provider `json:"provider"`
	Status      Status           `json:"status"`
	AccountHint string           `json:"account_hint"`
	PlanType    string           `json:"plan_type"`
	LastSuccess *time.Time       `json:"last_success,omitempty"`
	Error       string           `json:"error,omitempty"`
}

// State is everything the popup renders.
type State struct {
	Version             string            `json:"version"`
	Paired              bool              `json:"paired"`
	Revoked             bool              `json:"revoked"`
	PersonName          string            `json:"person_name"`
	DeviceName          string            `json:"device_name"`
	ServerURL           string            `json:"server_url"`
	Providers           []ProviderState   `json:"providers"`
	PendingUploads      int               `json:"pending_uploads"`
	LastSyncAt          *time.Time        `json:"last_sync_at,omitempty"`
	SyncError           string            `json:"sync_error,omitempty"`
	Overview            *syncapi.Overview `json:"overview,omitempty"`
	Local               []LocalAccount    `json:"local"`
	StatusLineInstalled bool              `json:"status_line_installed"`
	LaunchAtLogin       bool              `json:"launch_at_login"`
	Language            string            `json:"language"`
	Update              *update.Release   `json:"update,omitempty"`
}

// LocalAccount is this device's latest quota reading, shown before pairing
// or while the server is unreachable.
type LocalAccount struct {
	Provider account.Provider         `json:"provider"`
	Hint     string                   `json:"hint"`
	PlanType string                   `json:"plan_type"`
	Buckets  []syncapi.BucketOverview `json:"buckets"`
}

type Agent struct {
	version string
	log     *slog.Logger
	store   *storage.Store
	dir     string

	// OnChange is called after state visible to the popup changes.
	OnChange func()

	kickSync chan struct{}
	kickScan chan struct{}

	mu        gosync.Mutex
	settings  settings.Settings
	providers map[account.Provider]*ProviderState
	client    *sync.Client
	revoked   bool
	lastSync  *time.Time
	syncErr   string
	overview  *syncapi.Overview
	update    *update.Release
}

func New(ctx context.Context, version string, log *slog.Logger) (*Agent, error) {
	dir, err := settings.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st, err := settings.Load()
	if err != nil {
		return nil, err
	}
	store, err := storage.Open(ctx, filepath.Join(dir, "local.db"))
	if err != nil {
		return nil, err
	}
	a := &Agent{
		version:  version,
		log:      log,
		store:    store,
		dir:      dir,
		settings: st,
		kickSync: make(chan struct{}, 1),
		kickScan: make(chan struct{}, 1),
		providers: map[account.Provider]*ProviderState{
			account.ProviderAnthropic: {Provider: account.ProviderAnthropic, Status: StatusNotInstalled},
			account.ProviderOpenAI:    {Provider: account.ProviderOpenAI, Status: StatusNotInstalled},
		},
	}
	if st.Paired() {
		token, err := secret.Token(st.DeviceID)
		if err != nil {
			log.Warn("device token unavailable; sync disabled until re-joined", "err", err)
		} else {
			a.client = sync.NewClient(st.ServerURL, token)
		}
	}
	return a, nil
}

func (a *Agent) Close() error { return a.store.Close() }

func (a *Agent) SpoolDir() string { return filepath.Join(a.dir, "claude-statusline") }

// Run blocks until ctx is done.
func (a *Agent) Run(ctx context.Context) {
	a.observeAll(ctx)
	a.scanAll(ctx)

	var wg gosync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); a.identityLoop(ctx) }()
	go func() { defer wg.Done(); a.updateLoop(ctx) }()
	go func() { defer wg.Done(); a.scanLoop(ctx) }()
	go func() { defer wg.Done(); a.syncLoop(ctx) }()
	wg.Wait()
}

// Refresh re-reads identities and logs and syncs now.
func (a *Agent) Refresh() {
	kick(a.kickScan)
	kick(a.kickSync)
}

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (a *Agent) changed() {
	if a.OnChange != nil {
		a.OnChange()
	}
}

func (a *Agent) identityLoop(ctx context.Context) {
	t := time.NewTicker(identityInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.observeAll(ctx)
		}
	}
}

func (a *Agent) scanLoop(ctx context.Context) {
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kickScan:
			a.observeAll(ctx)
			a.scanAll(ctx)
		case <-t.C:
			a.scanAll(ctx)
		}
	}
}

func (a *Agent) syncLoop(ctx context.Context) {
	backoff := syncInterval
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kickSync:
		case <-timer.C:
		}
		if err := a.syncOnce(ctx); err != nil {
			backoff = min(backoff*2, maxSyncBackoff)
		} else {
			backoff = syncInterval
		}
		timer.Reset(backoff)
	}
}

func (a *Agent) updateLoop(ctx context.Context) {
	for {
		rel, err := update.Latest(ctx, a.version)
		if err != nil {
			a.log.Warn("check for updates", "err", err)
		} else {
			a.mu.Lock()
			a.update = rel
			a.mu.Unlock()
			a.changed()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateInterval):
		}
	}
}

func (a *Agent) setProvider(p account.Provider, update func(*ProviderState)) {
	a.mu.Lock()
	update(a.providers[p])
	a.mu.Unlock()
	a.changed()
}

func (a *Agent) recordError(p account.Provider, err error) {
	a.log.Warn("provider error", "provider", p, "err", err)
	a.setProvider(p, func(s *ProviderState) {
		s.Status = StatusError
		s.Error = err.Error()
	})
}
