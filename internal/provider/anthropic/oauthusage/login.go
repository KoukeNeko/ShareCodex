package oauthusage

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

// ShareCodex signs in with Claude Code's public OAuth client, as other tools
// that read these limits do. The sign-in is ShareCodex's own: it gets its own
// tokens and never touches the ones Claude Code keeps.
const (
	clientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	authorizeURL = "https://claude.ai/oauth/authorize"
	// scopes is Claude Code's set without org:create_api_key, which reading
	// usage has no use for.
	scopes = "user:profile user:inference"
	// refreshMargin renews a token this long before it expires, so a probe
	// never sends one that lapses in flight.
	refreshMargin = 5 * time.Minute
	// loginTimeout ends a sign-in nobody finishes in the browser.
	loginTimeout = 10 * time.Minute
)

// The endpoints are variables so tests can point them at a local server.
var (
	tokenURL   = "https://api.anthropic.com/v1/oauth/token"
	profileURL = "https://api.anthropic.com/api/oauth/profile"
)

// ErrLoginExpired means Anthropic no longer accepts a sign-in's refresh
// token; the account has to be signed in again.
var ErrLoginExpired = errors.New("Claude sign-in expired; sign in again")

// Tokens is one ShareCodex sign-in. Unlike Claude Code's credential, these
// belong to ShareCodex, so it refreshes them itself.
type Tokens struct {
	Access    string    `json:"access_token"`
	Refresh   string    `json:"refresh_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Profile identifies the account a sign-in belongs to.
type Profile struct {
	// OrgID is the organization Claude Code's `auth status` reports too, so
	// both resolve to the same account.
	OrgID    string
	Email    string
	PlanType string
}

// Login is one browser sign-in waiting for its authorization code.
type Login struct {
	// URL is the page the user approves the sign-in on.
	URL         string
	verifier    string
	state       string
	redirectURI string
	server      *http.Server
	codes       chan string
}

// StartLogin listens for the browser's redirect on a loopback port and
// returns the sign-in page to open. Where no browser can reach this machine,
// the code shown on the page can be passed to Submit instead.
func StartLogin() (*Login, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for sign-in redirect: %w", err)
	}
	verifier, err := randomString(32)
	if err != nil {
		ln.Close()
		return nil, err
	}
	state, err := randomString(32)
	if err != nil {
		ln.Close()
		return nil, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	l := &Login{
		verifier:    verifier,
		state:       state,
		redirectURI: fmt.Sprintf("http://localhost:%d/callback", ln.Addr().(*net.TCPAddr).Port),
		codes:       make(chan string, 1),
	}
	l.URL = authorizeURL + "?" + url.Values{
		"code":                  {"true"},
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {l.redirectURI},
		"scope":                 {scopes},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}.Encode()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != l.state || q.Get("code") == "" {
			http.Error(w, "This sign-in link is not the one ShareCodex started.", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Signed in to ShareCodex. You can close this tab.")
		l.Submit(q.Get("code"))
	})
	l.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go l.server.Serve(ln)
	return l, nil
}

// Submit hands over an authorization code, or the page's redirect URL, typed
// in by the user. Only the first code counts.
func (l *Login) Submit(input string) {
	input = strings.TrimSpace(input)
	if u, err := url.Parse(input); err == nil && u.Query().Get("code") != "" {
		input = u.Query().Get("code")
	}
	if input == "" {
		return
	}
	select {
	case l.codes <- input:
	default:
	}
}

// Close stops waiting for the browser.
func (l *Login) Close() { l.server.Close() }

// Wait exchanges the first code received for tokens and reads whose account
// they are.
func (l *Login) Wait(ctx context.Context) (Tokens, Profile, error) {
	defer l.Close()
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	var code string
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Tokens{}, Profile{}, errors.New("Claude sign-in was not finished in time")
		}
		return Tokens{}, Profile{}, ctx.Err()
	case code = <-l.codes:
	}
	// The code page shows "code#state".
	state := l.state
	if c, s, ok := strings.Cut(code, "#"); ok {
		code = c
		if s != "" {
			state = s
		}
	}
	if state != l.state {
		return Tokens{}, Profile{}, errors.New("the pasted code belongs to another sign-in")
	}
	t, err := requestTokens(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  l.redirectURI,
		"code_verifier": l.verifier,
	}, "")
	if err != nil {
		return Tokens{}, Profile{}, err
	}
	p, err := fetchProfile(ctx, t.Access)
	if err != nil {
		return Tokens{}, Profile{}, err
	}
	return t, p, nil
}

// LinkedUsage reads a ShareCodex sign-in's rate limits, renewing its tokens
// first when they are about to expire. When it renews them, the new tokens
// are returned with refreshed set, and the caller must keep them: the old
// refresh token stops working.
func LinkedUsage(ctx context.Context, t Tokens, now time.Time) (buckets []quota.Bucket, renewed Tokens, refreshed bool, err error) {
	if !now.Add(refreshMargin).Before(t.ExpiresAt) {
		if t, err = requestTokens(ctx, map[string]string{
			"grant_type":    "refresh_token",
			"client_id":     clientID,
			"refresh_token": t.Refresh,
		}, t.Refresh); err != nil {
			return nil, Tokens{}, false, err
		}
		refreshed = true
	}
	buckets, err = fetchUsage(ctx, t.Access)
	return buckets, t, refreshed, err
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// requestTokens posts to the token endpoint. keepRefresh is reused when the
// answer carries no new refresh token.
func requestTokens(ctx context.Context, body map[string]string, keepRefresh string) (Tokens, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return Tokens{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(b))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("Claude sign-in: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Tokens{}, fmt.Errorf("Claude sign-in: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error == "invalid_grant" {
			return Tokens{}, ErrLoginExpired
		}
		return Tokens{}, fmt.Errorf("Claude sign-in: %s", resp.Status)
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return Tokens{}, fmt.Errorf("decode Claude sign-in: %w", err)
	}
	if tr.AccessToken == "" {
		return Tokens{}, errors.New("Claude sign-in returned no access token")
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	return Tokens{
		Access:    tr.AccessToken,
		Refresh:   cmp.Or(tr.RefreshToken, keepRefresh),
		ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC(),
	}, nil
}

type profileResponse struct {
	Account struct {
		Email        string `json:"email"`
		HasClaudeMax bool   `json:"has_claude_max"`
		HasClaudePro bool   `json:"has_claude_pro"`
	} `json:"account"`
	Organization struct {
		UUID             string `json:"uuid"`
		OrganizationType string `json:"organization_type"`
	} `json:"organization"`
}

func fetchProfile(ctx context.Context, token string) (Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", betaHeaders)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("read Claude profile: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Profile{}, fmt.Errorf("read Claude profile: %s", resp.Status)
	}
	var pr profileResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&pr); err != nil {
		return Profile{}, fmt.Errorf("decode Claude profile: %w", err)
	}
	if pr.Organization.UUID == "" && pr.Account.Email == "" {
		return Profile{}, errors.New("Claude profile names no account")
	}
	return Profile{OrgID: pr.Organization.UUID, Email: pr.Account.Email, PlanType: planType(pr)}, nil
}

// planType matches the subscriptionType `claude auth status` reports.
func planType(pr profileResponse) string {
	switch {
	case pr.Account.HasClaudeMax:
		return "max"
	case pr.Account.HasClaudePro:
		return "pro"
	}
	return strings.TrimPrefix(pr.Organization.OrganizationType, "claude_")
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
