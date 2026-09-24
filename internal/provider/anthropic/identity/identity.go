// Package identity asks Claude Code which account it is signed into, using
// `claude auth status --json` rather than reading credential files.
package identity

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/provider"
)

type authStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	OrgID            string `json:"orgId"`
	SubscriptionType string `json:"subscriptionType"`
}

// Observe returns the current account. When Claude Code is signed out or
// uses an API key, ExternalRefHash is empty: that usage is not on a pooled
// subscription.
func Observe(ctx context.Context) (account.Observation, error) {
	bin, err := provider.LookPath("claude")
	if err != nil {
		return account.Observation{}, fmt.Errorf("claude CLI not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "status", "--json").Output()
	if err != nil {
		return account.Observation{}, fmt.Errorf("claude auth status: %w", err)
	}
	return parse(out, time.Now())
}

func parse(out []byte, now time.Time) (account.Observation, error) {
	var st authStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return account.Observation{}, fmt.Errorf("decode claude auth status: %w", err)
	}
	o := account.Observation{Provider: account.ProviderAnthropic, ObservedAt: now}
	ref := cmp.Or(st.OrgID, st.Email)
	if !st.LoggedIn || st.AuthMethod != "claude.ai" || ref == "" {
		return o, nil
	}
	o.ExternalRefHash = account.HashExternalRef(account.ProviderAnthropic, ref)
	o.Hint = account.MaskRef(cmp.Or(st.Email, st.OrgID))
	o.PlanType = st.SubscriptionType
	return o, nil
}
