// Package oauthusage reads an account's rate limits from Anthropic's own
// usage endpoint, the one Claude Code's /usage shows. Claude Desktop runs no
// statusLine and keeps its credential to itself, so its usage produces no
// readings from logs or spools; this probe reports the account Claude Code is
// signed into without any session running, which covers Desktop usage
// whenever that account is signed into Claude Code on a machine running the
// agent.
package oauthusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/config"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

// usageURL is Anthropic's usage endpoint, which answers with the same windows
// the Claude usage page and Claude Code's /usage display. It is a variable so
// tests can point it at a local server.
var usageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	// cliVersion is the Claude Code release the request presents itself as.
	// The endpoint reads the version and gates response blocks on it, so it
	// needs bumping together with a live check.
	cliVersion = "2.1.283"
	// betaHeaders are the beta features Claude Code asks for.
	betaHeaders = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05"
	// requestTimeout bounds one probe. The endpoint is rate limited, so a
	// slow answer is dropped rather than retried.
	requestTimeout = 30 * time.Second
	// maxBody caps the answer; a usage report is a small JSON object.
	maxBody = 1 << 20
)

// ErrNoCredential means Claude Code is not signed in on this machine, so
// there is nothing to probe.
var ErrNoCredential = errors.New("no Claude Code credential")

// Credential is the sign-in Claude Code keeps for its own use.
type Credential struct {
	Token     string
	ExpiresAt time.Time
	// PlanType is the plan with its Max tier, such as "max 20x".
	PlanType string
}

type credentialFile struct {
	OAuth *struct {
		AccessToken      string `json:"accessToken"`
		ExpiresAt        int64  `json:"expiresAt"`
		SubscriptionType string `json:"subscriptionType"`
		RateLimitTier    string `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

// PlanWithTier adds the Max tier to a plan: "max" with the rate-limit tier
// "default_claude_max_20x" becomes "max 20x". Other plans are unchanged.
func PlanWithTier(plan, tier string) string {
	if plan != "max" {
		return plan
	}
	for _, t := range []string{"20x", "5x"} {
		if strings.HasSuffix(tier, "_max_"+t) {
			return plan + " " + t
		}
	}
	return plan
}

// Probe returns the account's current rate-limit buckets and its plan. It
// returns no buckets when there is nothing to read: Claude Code is not
// signed in, or its credential expired before the CLI's own background
// refresh renewed it.
func Probe(ctx context.Context, now time.Time) ([]quota.Bucket, string, error) {
	c, err := readCredential(ctx)
	if errors.Is(err, ErrNoCredential) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	// Claude Code's supervisor refreshes the credential in the background,
	// so an expired one only means nothing has renewed it yet.
	if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
		return nil, "", nil
	}
	buckets, err := fetchUsage(ctx, c.Token)
	return buckets, c.PlanType, err
}

// readCredential returns the credential of the profile the agent reports
// for: the file of an explicit CLAUDE_CONFIG_DIR profile, else the OS
// keychain, else the default profile's file.
func readCredential(ctx context.Context) (Credential, error) {
	raw, err := readRaw(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, err
	}
	return parseCredential(raw)
}

func readRaw(ctx context.Context) ([]byte, error) {
	if os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		b, err := keychainCredential(ctx)
		if err == nil {
			return b, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	path, err := credentialPath()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// credentialPath is the credential file Claude Code writes where the OS
// keychain is unavailable.
func credentialPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".credentials.json"), nil
}

func parseCredential(raw []byte) (Credential, error) {
	var f credentialFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return Credential{}, fmt.Errorf("decode Claude Code credential: %w", err)
	}
	if f.OAuth == nil || f.OAuth.AccessToken == "" {
		return Credential{}, ErrNoCredential
	}
	return Credential{
		Token:     f.OAuth.AccessToken,
		ExpiresAt: time.UnixMilli(f.OAuth.ExpiresAt).UTC(),
		PlanType:  PlanWithTier(f.OAuth.SubscriptionType, f.OAuth.RateLimitTier),
	}, nil
}

type window struct {
	Utilization *float64 `json:"utilization"`
	// ResetsAt is an RFC 3339 timestamp, not an epoch.
	ResetsAt string `json:"resets_at"`
}

type usage struct {
	FiveHour *window `json:"five_hour"`
	SevenDay *window `json:"seven_day"`
}

// fetchUsage asks Anthropic for the account's rate limits with the given
// access token.
func fetchUsage(ctx context.Context, token string) ([]quota.Bucket, error) {
	return getUsage(ctx, usageURL, token)
}

func getUsage(ctx context.Context, url, token string) ([]quota.Bucket, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "claude-cli/"+cliVersion+" (external, cli)")
	req.Header.Set("anthropic-beta", betaHeaders)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read Claude usage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Throttled or refused: the account keeps the reading already
		// recorded, and the next probe tries again.
		return nil, fmt.Errorf("read Claude usage: %s", resp.Status)
	}
	var u usage
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&u); err != nil {
		return nil, fmt.Errorf("decode Claude usage: %w", err)
	}
	return buckets(u), nil
}

// buckets turns the reported windows into the ledger's buckets. A window the
// account does not report is left out, and a window without a percentage
// stays null so the popup shows it as unknown rather than as zero.
func buckets(u usage) []quota.Bucket {
	var out []quota.Bucket
	add := func(key quota.BucketKey, minutes int, w *window) {
		if w == nil {
			return
		}
		mins := minutes
		b := quota.Bucket{Key: key, UsedPercent: w.Utilization, WindowMinutes: &mins}
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
			t = t.UTC()
			b.ResetsAt = &t
		}
		out = append(out, b)
	}
	add(quota.BucketFiveHour, quota.FiveHourMinutes, u.FiveHour)
	add(quota.BucketWeekly, quota.WeeklyMinutes, u.SevenDay)
	return out
}
