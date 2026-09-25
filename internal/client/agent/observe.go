package agent

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/desktop"
	claudeidentity "github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/identity"
	"github.com/KoukeNeko/ShareCodex/internal/provider/openai/codex/appserver"
)

// observeAll records which account each CLI is signed into. Codex also
// reports its current rate limits in the same call.
func (a *Agent) observeAll(ctx context.Context) {
	a.observe(ctx, account.ProviderAnthropic)
	a.observe(ctx, account.ProviderOpenAI)
	a.observeDesktop(ctx)
}

// observeDesktop records the account Claude Desktop last used, at the time
// it was last active. Desktop's sign-in cannot be asked for without reading
// its credentials, so the most recent Claude Code session stands in for it.
// Re-recording the same session is a no-op.
func (a *Agent) observeDesktop(ctx context.Context) {
	dir, err := desktop.Dir()
	if err != nil {
		a.log.Warn("locate Claude Desktop", "err", err)
		return
	}
	sessions, err := desktop.Sessions(dir)
	if err != nil {
		a.log.Warn("read Claude Desktop sessions", "err", err)
		return
	}
	latest, ok := desktop.Latest(sessions)
	if !ok {
		return
	}
	o := account.Observation{
		Provider:        account.ProviderAnthropic,
		Source:          account.SourceClaudeDesktop,
		ExternalRefHash: account.HashExternalRef(account.ProviderAnthropic, latest.OrgID),
		ObservedAt:      latest.LastActivity,
	}
	if err := a.store.AddObservation(ctx, o); err != nil {
		a.log.Error("save Claude Desktop account", "err", err)
		return
	}
	kick(a.kickSync)
}

func (a *Agent) observe(ctx context.Context, p account.Provider) {
	var (
		o   account.Observation
		err error
	)
	switch p {
	case account.ProviderAnthropic:
		o, err = claudeidentity.Observe(ctx)
	case account.ProviderOpenAI:
		var res appserver.Result
		res, err = appserver.Read(ctx, a.version)
		o = res.Observation
		if err == nil && res.Snapshot != nil {
			if err := a.store.AddSnapshot(ctx, *res.Snapshot); err != nil {
				a.log.Error("save codex rate limits", "err", err)
			}
		}
	}
	if errors.Is(err, exec.ErrNotFound) {
		a.setProvider(p, func(s *ProviderState) { *s = ProviderState{Provider: p, Status: StatusNotInstalled} })
		return
	}
	if err != nil {
		a.recordError(p, err)
		return
	}
	if err := a.store.AddObservation(ctx, o); err != nil {
		a.recordError(p, err)
		return
	}
	now := time.Now()
	a.setProvider(p, func(s *ProviderState) {
		s.Error = ""
		s.LastSuccess = &now
		s.AccountHint, s.PlanType = o.Hint, o.PlanType
		s.Status = StatusOK
		if o.ExternalRefHash == "" {
			s.Status = StatusNotPooled
		}
	})
	kick(a.kickSync)
}

// lastObservedAt is when the provider's identity was last recorded.
func lastObservedAt(obs []account.Observation) time.Time {
	if len(obs) == 0 {
		return time.Time{}
	}
	return obs[len(obs)-1].ObservedAt
}
