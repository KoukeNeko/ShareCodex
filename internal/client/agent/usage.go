package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/client/secret"
	"github.com/KoukeNeko/ShareCodex/internal/client/storage"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/oauthusage"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

// probeUsage records the rate limits Anthropic reports for the account
// Claude Code is signed into and for every account signed in to ShareCodex.
// Claude Desktop runs no statusLine, so usage that only Desktop produces is
// read here instead, without any session.
func (a *Agent) probeUsage(ctx context.Context) {
	covered, recorded := a.probeClaudeCode(ctx)

	logins, err := a.store.ClaudeLogins(ctx)
	if err != nil {
		a.log.Error("load Claude sign-ins", "err", err)
	}
	for _, l := range logins {
		if l.RefHash == covered {
			continue
		}
		buckets, err := a.probeLogin(ctx, l)
		if err != nil {
			a.log.Warn("read Claude usage", "account", l.Hint, "err", err)
			continue
		}
		recorded = recorded || len(buckets) > 0
	}
	if recorded {
		kick(a.kickSync)
		a.changed()
	}
}

// probeClaudeCode reads the account Claude Code is signed into and returns
// it, so the same account is not read twice.
func (a *Agent) probeClaudeCode(ctx context.Context) (string, bool) {
	ac, err := a.loadAccounts(ctx, account.ProviderAnthropic)
	if err != nil {
		a.log.Error("load Claude accounts", "err", err)
		return "", false
	}
	// The credential is whatever Claude Code is signed into now, so the
	// timeline must be current too, or a reading taken just after an
	// account switch lands on the previous account.
	if time.Since(lastObservedAt(ac.cli)) > time.Minute {
		a.observe(ctx, account.ProviderAnthropic)
		if ac, err = a.loadAccounts(ctx, account.ProviderAnthropic); err != nil {
			a.log.Error("load Claude accounts", "err", err)
			return "", false
		}
	}
	if !ac.pooled() {
		return "", false
	}
	now := time.Now()
	// The probe has no session to attribute, so its reading follows the
	// CLI's sign-in timeline, exactly as Codex's rollout snapshots do.
	ref, ok := ac.resolve("", "", now)
	if !ok {
		return "", false
	}
	buckets, err := oauthusage.Probe(ctx, now)
	if err != nil {
		a.log.Warn("read Claude usage", "err", err)
		return "", false
	}
	if len(buckets) == 0 {
		return "", false
	}
	err = a.store.AddSnapshot(ctx, quota.Snapshot{
		AccountRefHash: ref,
		Provider:       account.ProviderAnthropic,
		ObservedAt:     now,
		Source:         quota.SourceClaudeOAuthUsage,
		Buckets:        buckets,
	})
	if err != nil {
		a.log.Error("save Claude usage", "err", err)
		return "", false
	}
	return ref, true
}

// probeLogin reads one ShareCodex sign-in, keeping its tokens current.
func (a *Agent) probeLogin(ctx context.Context, l storage.ClaudeLogin) ([]quota.Bucket, error) {
	raw, err := secret.ClaudeLogin(l.RefHash)
	if errors.Is(err, secret.ErrNotFound) {
		return nil, errors.New("sign-in missing from the credential store; sign in again")
	}
	if err != nil {
		return nil, err
	}
	var tokens oauthusage.Tokens
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return nil, fmt.Errorf("decode stored sign-in: %w", err)
	}
	now := time.Now()
	buckets, renewed, refreshed, err := oauthusage.LinkedUsage(ctx, tokens, now)
	if refreshed {
		// The old refresh token is spent, so the new one is kept even if
		// the usage read itself failed.
		if err := saveLoginTokens(l.RefHash, renewed); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	if len(buckets) == 0 {
		return nil, nil
	}
	err = a.store.AddSnapshot(ctx, quota.Snapshot{
		AccountRefHash: l.RefHash,
		AccountHint:    l.Hint,
		PlanType:       l.PlanType,
		Provider:       account.ProviderAnthropic,
		ObservedAt:     now,
		Source:         quota.SourceClaudeOAuthUsage,
		Buckets:        buckets,
	})
	return buckets, err
}

// LinkClaude keeps a finished ShareCodex sign-in and reads its account's
// limits once, so the account shows up without waiting for the next probe.
func (a *Agent) LinkClaude(ctx context.Context, tokens oauthusage.Tokens, p oauthusage.Profile) (storage.ClaudeLogin, []quota.Bucket, error) {
	l := storage.ClaudeLogin{
		RefHash:  account.HashExternalRef(account.ProviderAnthropic, cmp.Or(p.OrgID, p.Email)),
		Hint:     account.MaskRef(cmp.Or(p.Email, p.OrgID)),
		PlanType: p.PlanType,
	}
	if err := saveLoginTokens(l.RefHash, tokens); err != nil {
		return l, nil, err
	}
	if err := a.store.AddClaudeLogin(ctx, l); err != nil {
		return l, nil, err
	}
	buckets, err := a.probeLogin(ctx, l)
	kick(a.kickSync)
	a.changed()
	return l, buckets, err
}

// ClaudeLogins lists the accounts signed in to ShareCodex.
func (a *Agent) ClaudeLogins(ctx context.Context) ([]storage.ClaudeLogin, error) {
	return a.store.ClaudeLogins(ctx)
}

// UnlinkClaude forgets a sign-in, named by its masked email as ClaudeLogins
// lists it. The account and its recorded readings stay.
func (a *Agent) UnlinkClaude(ctx context.Context, hint string) error {
	logins, err := a.store.ClaudeLogins(ctx)
	if err != nil {
		return err
	}
	for _, l := range logins {
		if l.Hint != hint {
			continue
		}
		if err := secret.DeleteClaudeLogin(l.RefHash); err != nil {
			return err
		}
		if err := a.store.RemoveClaudeLogin(ctx, l.RefHash); err != nil {
			return err
		}
		a.changed()
		return nil
	}
	return fmt.Errorf("no Claude sign-in %q", hint)
}

func saveLoginTokens(refHash string, t oauthusage.Tokens) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := secret.SetClaudeLogin(refHash, string(b)); err != nil {
		return fmt.Errorf("save Claude sign-in: %w", err)
	}
	return nil
}
