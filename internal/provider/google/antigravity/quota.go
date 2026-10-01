package antigravity

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

// modelsURL is the endpoint agy itself asks for its models; each comes with
// the quota left in the pool it draws on.
const modelsURL = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"

// userAgent names the client; Google answers only Antigravity's.
const userAgent = "antigravity"

// keyringPrefix marks a value go-keyring stored base64-encoded, as agy's
// credential is on macOS.
const keyringPrefix = "go-keyring-base64:"

// credential is agy's sign-in as it stores it.
type credential struct {
	Token struct {
		AccessToken string    `json:"access_token"`
		Expiry      time.Time `json:"expiry"`
	} `json:"token"`
}

// ErrExpired is returned when agy's access token has expired. agy renews it
// when it next runs; ShareCodex never renews another app's sign-in.
var ErrExpired = errors.New("Antigravity's sign-in has expired; it renews when agy next runs")

// Antigravity's quota pools: every Gemini model draws on one, and the other
// vendors' models (Claude, GPT-OSS) on another. Each runs a 5-hour window.
var pools = []string{"gemini", "claude"}

// Quota reads the quota left in each of Antigravity's pools for the account
// agy is signed into. It returns fs.ErrNotExist when agy is not signed in on
// this computer, and ErrExpired when its sign-in needs renewing.
func Quota(ctx context.Context, now time.Time) ([]quota.Bucket, error) {
	raw, err := storedCredential(ctx)
	if err != nil {
		return nil, err
	}
	var cred credential
	if err := json.Unmarshal([]byte(strings.TrimSpace(decodeKeyring(raw))), &cred); err != nil {
		return nil, fmt.Errorf("read Antigravity's sign-in: %w", err)
	}
	if cred.Token.AccessToken == "" {
		return nil, fs.ErrNotExist
	}
	if !cred.Token.Expiry.IsZero() && !now.Before(cred.Token.Expiry) {
		return nil, ErrExpired
	}
	body, err := json.Marshal(map[string]string{"project": projectID()})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.Token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read Antigravity quota: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("read Antigravity quota: %s", resp.Status)
	}
	var out modelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode Antigravity quota: %w", err)
	}
	return out.buckets(), nil
}

type modelsResponse struct {
	Models map[string]struct {
		QuotaInfo *struct {
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
		} `json:"quotaInfo"`
	} `json:"models"`
}

// buckets turns the models' quota into one bucket per pool. Every model of a
// pool reports the same figures; the lowest remaining is taken, should they
// ever differ.
func (r modelsResponse) buckets() []quota.Bucket {
	var out []quota.Bucket
	for _, family := range pools {
		var used *float64
		var resets *time.Time
		for id, m := range r.Models {
			q := m.QuotaInfo
			if !quota.InFamily(id, family) || q == nil || q.RemainingFraction == nil || q.ResetTime == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339, q.ResetTime)
			if err != nil {
				continue
			}
			u := (1 - *q.RemainingFraction) * 100
			if used == nil || u > *used {
				used, resets = &u, &at
			}
		}
		if used == nil {
			continue
		}
		window := quota.FiveHourMinutes
		out = append(out, quota.Bucket{Key: quota.FiveHourModelBucket(family), UsedPercent: used, ResetsAt: resets, WindowMinutes: &window})
	}
	return out
}

// decodeKeyring undoes go-keyring's encoding of a stored value.
func decodeKeyring(raw string) string {
	raw = strings.TrimSpace(raw)
	encoded, ok := strings.CutPrefix(raw, keyringPrefix)
	if !ok {
		return raw
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return raw
	}
	return string(b)
}

// projectID is the Google Cloud project agy runs under, if it chose one.
func projectID() string {
	dir, err := geminiDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "antigravity-cli", "cache", "default_project_id.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
