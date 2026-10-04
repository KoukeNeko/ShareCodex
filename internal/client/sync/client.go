// Package sync is the client side of the sync protocol: pairing with a
// join link, uploading outbox batches, and fetching the overview.
package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

var (
	ErrRevoked       = errors.New("this device was revoked by an admin")
	ErrUpgrade       = errors.New("the server needs a newer ShareCodex")
	ErrInvalidInvite = errors.New("join link is invalid, expired or already used")
	ErrServerTooOld  = errors.New("the server needs a newer ShareCodex for this; ask an admin to update it")
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// ParseJoinLink splits https://host/join/<code> into the server base URL
// and the invite code.
func ParseJoinLink(link string) (baseURL, code string, err error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", "", errors.New("join link must be an http(s) URL")
	}
	prefix, code, ok := strings.Cut(u.Path, syncapi.PathJoin)
	if !ok || code == "" || strings.Contains(code, "/") {
		return "", "", errors.New("join link must end in /join/<code>")
	}
	u.Path, u.RawQuery, u.Fragment = prefix, "", ""
	return strings.TrimRight(u.String(), "/"), code, nil
}

func Pair(ctx context.Context, link, deviceName, platform string) (syncapi.PairResponse, string, error) {
	base, code, err := ParseJoinLink(link)
	if err != nil {
		return syncapi.PairResponse{}, "", err
	}
	c := NewClient(base, "")
	var resp syncapi.PairResponse
	err = c.do(ctx, http.MethodPost, syncapi.PathPair, syncapi.PairRequest{
		Version: syncapi.Version, Code: code, DeviceName: deviceName, Platform: platform,
	}, &resp)
	if errors.Is(err, errForbidden) {
		return syncapi.PairResponse{}, "", ErrInvalidInvite
	}
	return resp, base, err
}

func (c *Client) Sync(ctx context.Context, req syncapi.SyncRequest) (syncapi.SyncResponse, error) {
	req.Version = syncapi.Version
	var resp syncapi.SyncResponse
	err := c.do(ctx, http.MethodPost, syncapi.PathSync, req, &resp)
	return resp, err
}

// Overview fetches the popup's data; today is the start of this device's
// day, so the viewer's "today" follows the local time zone.
func (c *Client) Overview(ctx context.Context, today time.Time) (syncapi.Overview, error) {
	var o syncapi.Overview
	q := url.Values{syncapi.QueryToday: {today.Format(time.RFC3339)}}
	err := c.do(ctx, http.MethodGet, syncapi.PathOverview+"?"+q.Encode(), nil, &o)
	return o, err
}

// Invite creates a join link code for another device of this person.
func (c *Client) Invite(ctx context.Context) (syncapi.InviteResponse, error) {
	var resp syncapi.InviteResponse
	err := c.do(ctx, http.MethodPost, syncapi.PathInvite, nil, &resp)
	if errors.Is(err, errNotFound) {
		return syncapi.InviteResponse{}, ErrServerTooOld
	}
	return resp, err
}

// MemberUsage fetches one member's usage over a period: 24h, 7d or 30d.
func (c *Client) MemberUsage(ctx context.Context, personID, period string) (syncapi.MemberUsage, error) {
	var u syncapi.MemberUsage
	q := url.Values{syncapi.QueryPeriod: {period}}
	err := c.do(ctx, http.MethodGet, syncapi.PathPeople+url.PathEscape(personID)+"/usage?"+q.Encode(), nil, &u)
	if errors.Is(err, errNotFound) {
		return syncapi.MemberUsage{}, ErrServerTooOld
	}
	return u, err
}

// AccountCapacity fetches an account's capacity analysis and scenario
// simulation over a period, or over [from, to) when both RFC 3339 instants
// are given.
func (c *Client) AccountCapacity(ctx context.Context, accountID, period, from, to string) (syncapi.CapacityReport, error) {
	var r syncapi.CapacityReport
	q := url.Values{syncapi.QueryPeriod: {period}}
	if from != "" && to != "" {
		q.Set(syncapi.QueryFrom, from)
		q.Set(syncapi.QueryTo, to)
	}
	err := c.do(ctx, http.MethodGet, syncapi.PathAccounts+url.PathEscape(accountID)+"/capacity?"+q.Encode(), nil, &r)
	if errors.Is(err, errNotFound) {
		return syncapi.CapacityReport{}, ErrServerTooOld
	}
	return r, err
}

// LeaveAccount takes this person out of a shared account.
func (c *Client) LeaveAccount(ctx context.Context, accountID string) error {
	err := c.do(ctx, http.MethodPost, syncapi.PathAccounts+url.PathEscape(accountID)+"/leave", nil, &struct{}{})
	if errors.Is(err, errNotFound) {
		return ErrServerTooOld
	}
	return err
}

var (
	errForbidden = errors.New("forbidden")
	errNotFound  = errors.New("server returned 404 Not Found")
)

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("reach server: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusUnauthorized:
		return ErrRevoked
	case http.StatusForbidden:
		return errForbidden
	case http.StatusNotFound:
		return errNotFound
	case http.StatusUpgradeRequired:
		return ErrUpgrade
	}
	var e syncapi.Error
	json.NewDecoder(resp.Body).Decode(&e)
	if e.Error == "" {
		e.Error = resp.Status
	}
	return fmt.Errorf("server returned %d: %s", resp.StatusCode, e.Error)
}
