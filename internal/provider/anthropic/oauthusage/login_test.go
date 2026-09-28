package oauthusage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// fakeAnthropic answers the token, profile and usage endpoints and records
// the last token request.
func fakeAnthropic(t *testing.T, token func(body map[string]string) (int, string)) *map[string]string {
	t.Helper()
	var last map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			last = map[string]string{}
			if err := json.NewDecoder(r.Body).Decode(&last); err != nil {
				t.Errorf("token request body: %v", err)
			}
			status, body := token(last)
			w.WriteHeader(status)
			w.Write([]byte(body))
		case "/profile":
			if r.Header.Get("Authorization") != "Bearer new-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"account":{"email":"alice@example.com","has_claude_max":true},
				"organization":{"uuid":"org-1","organization_type":"claude_max"}}`))
		case "/usage":
			if r.Header.Get("Authorization") != "Bearer new-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(sample))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	for _, u := range []struct {
		v    *string
		path string
	}{{&tokenURL, "/token"}, {&profileURL, "/profile"}, {&usageURL, "/usage"}} {
		old := *u.v
		*u.v = srv.URL + u.path
		t.Cleanup(func() { *u.v = old })
	}
	return &last
}

func issued(map[string]string) (int, string) {
	return http.StatusOK, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`
}

func TestLoginExchangesTheRedirectedCode(t *testing.T) {
	last := fakeAnthropic(t, issued)
	l, err := StartLogin()
	if err != nil {
		t.Fatal(err)
	}
	page, _ := url.Parse(l.URL)
	q := page.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != l.redirectURI {
		t.Fatalf("sign-in page = %s", l.URL)
	}

	// A redirect that did not come from this sign-in is refused.
	resp, err := http.Get(l.redirectURI + "?code=stolen&state=other")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign redirect = %d, want 400", resp.StatusCode)
	}
	resp, err = http.Get(l.redirectURI + "?code=abc&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokens, profile, err := l.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if (*last)["code"] != "abc" || (*last)["grant_type"] != "authorization_code" || (*last)["code_verifier"] != l.verifier {
		t.Errorf("token request = %v", *last)
	}
	if tokens.Access != "new-access" || tokens.Refresh != "new-refresh" || !tokens.ExpiresAt.After(time.Now()) {
		t.Errorf("tokens = %+v", tokens)
	}
	want := Profile{OrgID: "org-1", Email: "alice@example.com", PlanType: "max"}
	if profile != want {
		t.Errorf("profile = %+v, want %+v", profile, want)
	}
}

func TestLoginAcceptsThePastedCode(t *testing.T) {
	last := fakeAnthropic(t, issued)
	l, err := StartLogin()
	if err != nil {
		t.Fatal(err)
	}
	// The code page shows "code#state" for a browser that cannot reach
	// this machine.
	l.Submit("  pasted#" + l.state + "\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := l.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if (*last)["code"] != "pasted" || (*last)["state"] != l.state {
		t.Errorf("token request = %v", *last)
	}

	other, err := StartLogin()
	if err != nil {
		t.Fatal(err)
	}
	other.Submit("pasted#" + l.state)
	if _, _, err := other.Wait(ctx); err == nil {
		t.Error("a code from another sign-in was accepted")
	}
}

func TestLinkedUsageRenewsExpiringTokens(t *testing.T) {
	last := fakeAnthropic(t, func(map[string]string) (int, string) {
		// No refresh_token in the answer: the old one stays valid.
		return http.StatusOK, `{"access_token":"new-access","expires_in":3600}`
	})
	now := time.Now()
	old := Tokens{Access: "old-access", Refresh: "keep", ExpiresAt: now.Add(time.Minute)}
	got, renewed, refreshed, err := LinkedUsage(context.Background(), old, now)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed || renewed.Access != "new-access" || renewed.Refresh != "keep" {
		t.Errorf("renewed = %+v, refreshed %v", renewed, refreshed)
	}
	if (*last)["grant_type"] != "refresh_token" || (*last)["refresh_token"] != "keep" {
		t.Errorf("token request = %v", *last)
	}
	if len(got) != 2 {
		t.Errorf("buckets = %+v", got)
	}

	*last = nil
	current := Tokens{Access: "new-access", Refresh: "keep", ExpiresAt: now.Add(time.Hour)}
	if _, _, refreshed, err := LinkedUsage(context.Background(), current, now); err != nil || refreshed || *last != nil {
		t.Errorf("a current token was renewed: refreshed %v, err %v, request %v", refreshed, err, *last)
	}
}

func TestLinkedUsageReportsARevokedSignIn(t *testing.T) {
	fakeAnthropic(t, func(map[string]string) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	})
	_, _, refreshed, err := LinkedUsage(context.Background(), Tokens{Refresh: "gone"}, time.Now())
	if !errors.Is(err, ErrLoginExpired) || refreshed {
		t.Errorf("err = %v, refreshed %v; want ErrLoginExpired", err, refreshed)
	}
}
