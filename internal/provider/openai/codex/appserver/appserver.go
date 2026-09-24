// Package appserver talks to `codex app-server` over stdio to learn which
// ChatGPT account Codex is signed into and its current rate limits. Codex
// handles authentication itself; credentials never pass through here.
package appserver

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/provider"
	"github.com/KoukeNeko/ShareCodex/internal/provider/openai/codex/rollout"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

type Result struct {
	Observation account.Observation
	// Snapshot is nil when the account is not a ChatGPT subscription or the
	// backend returned no windows.
	Snapshot *quota.Snapshot
}

type accountReadResult struct {
	Account *struct {
		Type     string `json:"type"`
		Email    string `json:"email"`
		PlanType string `json:"planType"`
	} `json:"account"`
}

type window struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type rateLimitsResult struct {
	AccountID  *string `json:"accountId"`
	RateLimits *struct {
		Primary   *window `json:"primary"`
		Secondary *window `json:"secondary"`
	} `json:"rateLimits"`
}

type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Read starts a short-lived app server, asks for the account and its rate
// limits, then stops it.
func Read(ctx context.Context, clientVersion string) (Result, error) {
	bin, err := provider.LookPath("codex")
	if err != nil {
		return Result{}, fmt.Errorf("codex CLI not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start codex app-server: %w", err)
	}
	defer func() {
		stdin.Close()
		cancel()
		cmd.Wait()
	}()

	c := &conn{w: stdin, r: bufio.NewReader(stdout)}
	if _, err := c.call(1, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "sharecodex", "version": clientVersion},
	}); err != nil {
		return Result{}, err
	}
	if err := c.send(map[string]any{"method": "initialized"}); err != nil {
		return Result{}, err
	}

	rawAccount, err := c.call(2, "account/read", map[string]any{})
	if err != nil {
		return Result{}, err
	}
	var acct accountReadResult
	if err := json.Unmarshal(rawAccount, &acct); err != nil {
		return Result{}, fmt.Errorf("decode account/read: %w", err)
	}

	now := time.Now()
	res := Result{Observation: account.Observation{Provider: account.ProviderOpenAI, ObservedAt: now}}
	if acct.Account == nil || acct.Account.Type != "chatgpt" {
		return res, nil
	}

	rawLimits, err := c.call(3, "account/rateLimits/read", map[string]any{"excludeResetCreditDetails": true})
	if err != nil {
		return Result{}, err
	}
	var limits rateLimitsResult
	if err := json.Unmarshal(rawLimits, &limits); err != nil {
		return Result{}, fmt.Errorf("decode account/rateLimits/read: %w", err)
	}

	accountID := ""
	if limits.AccountID != nil {
		accountID = *limits.AccountID
	}
	ref := cmp.Or(accountID, acct.Account.Email)
	if ref == "" {
		return res, nil
	}
	res.Observation.ExternalRefHash = account.HashExternalRef(account.ProviderOpenAI, ref)
	res.Observation.Hint = account.MaskRef(cmp.Or(acct.Account.Email, accountID))
	res.Observation.PlanType = acct.Account.PlanType

	if limits.RateLimits != nil {
		var buckets []quota.Bucket
		for _, w := range []*window{limits.RateLimits.Primary, limits.RateLimits.Secondary} {
			if w != nil {
				buckets = append(buckets, rollout.BucketFromWindow(w.UsedPercent, w.WindowDurationMins, w.ResetsAt))
			}
		}
		if len(buckets) > 0 {
			res.Snapshot = &quota.Snapshot{
				AccountRefHash: res.Observation.ExternalRefHash,
				Provider:       account.ProviderOpenAI,
				ObservedAt:     now,
				Source:         quota.SourceCodexRPC,
				Buckets:        buckets,
			}
		}
	}
	return res, nil
}

type conn struct {
	w io.Writer
	r *bufio.Reader
}

func (c *conn) send(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// call sends a request and returns its result, skipping notifications the
// server interleaves on the same stream.
func (c *conn) call(id int, method string, params any) (json.RawMessage, error) {
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%s: codex app-server closed the connection", method)
			}
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil || resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}
