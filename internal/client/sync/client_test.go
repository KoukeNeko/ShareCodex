package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

func TestParseJoinLink(t *testing.T) {
	tests := []struct {
		link, base, code string
		ok               bool
	}{
		{"https://pool.example.com/join/abc123", "https://pool.example.com", "abc123", true},
		{"  https://example.com/sharecodex/join/abc  ", "https://example.com/sharecodex", "abc", true},
		{"http://10.0.0.2:8080/join/xyz?utm=1", "http://10.0.0.2:8080", "xyz", true},
		{"https://example.com/join/", "", "", false},
		{"example.com/join/abc", "", "", false},
		{"ftp://example.com/join/abc", "", "", false},
	}
	for _, tt := range tests {
		base, code, err := ParseJoinLink(tt.link)
		if (err == nil) != tt.ok || base != tt.base || code != tt.code {
			t.Errorf("ParseJoinLink(%q) = %q, %q, %v", tt.link, base, code, err)
		}
	}
}

func TestMemberUsage(t *testing.T) {
	var gotPath, gotPeriod, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotPeriod, gotAuth = r.URL.EscapedPath(), r.URL.Query().Get(syncapi.QueryPeriod), r.Header.Get("Authorization")
		if r.URL.Path == syncapi.PathPeople+"gone/usage" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(syncapi.MemberUsage{PersonID: "a/b", Name: "alice", Period: gotPeriod})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "token")

	u, err := c.MemberUsage(context.Background(), "a/b", "7d")
	if err != nil || u.Name != "alice" || u.Period != "7d" {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	// The ID is one path segment, whatever it holds, and the call is signed.
	if gotPath != syncapi.PathPeople+"a%2Fb/usage" || gotPeriod != "7d" || gotAuth != "Bearer token" {
		t.Errorf("request = path %q, period %q, auth %q", gotPath, gotPeriod, gotAuth)
	}
	// A server without the endpoint answers 404, which means it is too old.
	if _, err := c.MemberUsage(context.Background(), "gone", "7d"); !errors.Is(err, ErrServerTooOld) {
		t.Errorf("missing endpoint error = %v, want ErrServerTooOld", err)
	}
}
