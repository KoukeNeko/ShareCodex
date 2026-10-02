package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/client/secret"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/client/storage"
	"github.com/KoukeNeko/ShareCodex/internal/client/sync"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// syncOnce uploads the whole outbox in batches, then refreshes the
// overview. Items are deleted only after the server accepted them, so a
// failure resends them; the server ignores duplicates. Only a failed upload
// fails the sync: a failed overview refresh leaves the last one on screen.
func (a *Agent) syncOnce(ctx context.Context) error {
	a.mu.Lock()
	client, revoked := a.client, a.revoked
	a.mu.Unlock()
	if client == nil || revoked {
		return nil
	}

	uploadErr := a.upload(ctx, client)
	var overviewErr error
	if uploadErr == nil {
		var o syncapi.Overview
		y, m, d := time.Now().Date()
		if o, overviewErr = client.Overview(ctx, time.Date(y, m, d, 0, 0, 0, 0, time.Local)); overviewErr == nil {
			a.mu.Lock()
			a.overview = &o
			a.mu.Unlock()
		}
	}

	now := time.Now()
	a.mu.Lock()
	switch {
	case errors.Is(uploadErr, sync.ErrRevoked):
		a.revoked = true
		a.syncErr = uploadErr.Error()
	case uploadErr != nil:
		a.syncErr = uploadErr.Error()
	default:
		a.syncErr = ""
		a.lastSync = &now
	}
	switch {
	case overviewErr != nil:
		a.overviewErr = overviewErr.Error()
	case uploadErr == nil:
		a.overviewErr = ""
	}
	a.mu.Unlock()
	if uploadErr != nil {
		a.log.Warn("sync failed", "err", uploadErr)
	}
	if overviewErr != nil {
		a.log.Warn("overview refresh failed", "err", overviewErr)
	}
	a.changed()
	return uploadErr
}

func (a *Agent) upload(ctx context.Context, client *sync.Client) error {
	for {
		items, err := a.store.PendingOutbox(ctx, batchSize)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		req, err := batchRequest(items)
		if err != nil {
			return err
		}
		res, err := client.Sync(ctx, req)
		if err != nil {
			return err
		}
		if res.Diverged > 0 {
			a.log.Warn("server kept another account for some events", "events", res.Diverged)
		}
		if err := a.store.DeleteOutboxThrough(ctx, items[len(items)-1].ID); err != nil {
			return err
		}
	}
}

func batchRequest(items []storage.OutboxItem) (syncapi.SyncRequest, error) {
	var req syncapi.SyncRequest
	for _, it := range items {
		var err error
		switch it.Kind {
		case storage.KindObservation:
			var o syncapi.Observation
			err = json.Unmarshal(it.Payload, &o)
			req.Observations = append(req.Observations, o)
		case storage.KindEvent:
			var e syncapi.Event
			err = json.Unmarshal(it.Payload, &e)
			req.Events = append(req.Events, e)
		case storage.KindSnapshot:
			var s syncapi.Snapshot
			err = json.Unmarshal(it.Payload, &s)
			req.Snapshots = append(req.Snapshots, s)
		default:
			err = fmt.Errorf("unknown outbox kind %q", it.Kind)
		}
		if err != nil {
			return syncapi.SyncRequest{}, fmt.Errorf("outbox item %d: %w", it.ID, err)
		}
	}
	return req, nil
}

// Join pairs this device using an admin's join link.
func (a *Agent) Join(ctx context.Context, link string) error {
	deviceName, err := os.Hostname()
	if err != nil || deviceName == "" {
		deviceName = runtime.GOOS
	}
	deviceName = strings.TrimSuffix(deviceName, ".local")

	resp, base, err := sync.Pair(ctx, link, deviceName, runtime.GOOS)
	if err != nil {
		return err
	}
	if err := secret.SetToken(resp.DeviceID, resp.Token); err != nil {
		return fmt.Errorf("save device token: %w", err)
	}

	a.mu.Lock()
	previous := a.settings.DeviceID
	a.settings.ServerURL = base
	a.settings.DeviceID = resp.DeviceID
	a.settings.DeviceName = deviceName
	a.settings.PersonID = resp.PersonID
	a.settings.PersonName = resp.PersonName
	st := a.settings
	a.client = sync.NewClient(base, resp.Token)
	a.revoked, a.syncErr, a.overviewErr, a.overview = false, "", "", nil
	a.mu.Unlock()

	if err := settings.Save(st); err != nil {
		return err
	}
	if previous != "" && previous != resp.DeviceID {
		if err := secret.DeleteToken(previous); err != nil {
			a.log.Warn("remove old device token", "err", err)
		}
	}
	a.Refresh()
	return nil
}

// Invite is a single-use join link for another of this person's devices.
type Invite struct {
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateInvite asks the server for a join link that adds another device
// for the same person.
func (a *Agent) CreateInvite(ctx context.Context) (Invite, error) {
	a.mu.Lock()
	client, revoked, base := a.client, a.revoked, a.settings.ServerURL
	a.mu.Unlock()
	if client == nil {
		return Invite{}, errors.New("join a server first")
	}
	if revoked {
		return Invite{}, sync.ErrRevoked
	}
	resp, err := client.Invite(ctx)
	if err != nil {
		return Invite{}, err
	}
	return Invite{Link: strings.TrimRight(base, "/") + syncapi.PathJoin + resp.Code, ExpiresAt: resp.ExpiresAt}, nil
}

// MemberUsage fetches one member's usage over a period (24h, 7d or 30d) for
// the popup's member view.
func (a *Agent) MemberUsage(ctx context.Context, personID, period string) (syncapi.MemberUsage, error) {
	a.mu.Lock()
	client, revoked := a.client, a.revoked
	a.mu.Unlock()
	if client == nil {
		return syncapi.MemberUsage{}, errors.New("join a server first")
	}
	if revoked {
		return syncapi.MemberUsage{}, sync.ErrRevoked
	}
	return client.MemberUsage(ctx, personID, period)
}

// LeaveAccount takes this person out of a shared account's allotment; the
// server then leaves it off their overview.
func (a *Agent) LeaveAccount(ctx context.Context, accountID string) error {
	a.mu.Lock()
	client, revoked := a.client, a.revoked
	a.mu.Unlock()
	if client == nil {
		return errors.New("join a server first")
	}
	if revoked {
		return sync.ErrRevoked
	}
	if err := client.LeaveAccount(ctx, accountID); err != nil {
		return err
	}
	// Drop the card now rather than after the next overview fetch.
	a.mu.Lock()
	if a.overview != nil {
		o := *a.overview
		o.Accounts = slices.DeleteFunc(slices.Clone(o.Accounts), func(ao syncapi.AccountOverview) bool { return ao.ID == accountID })
		a.overview = &o
	}
	a.mu.Unlock()
	a.changed()
	a.Refresh()
	return nil
}

// Leave forgets the server on this device. Usage already uploaded stays on
// the server; an admin revokes the device there.
func (a *Agent) Leave() error {
	a.mu.Lock()
	deviceID := a.settings.DeviceID
	a.settings.ServerURL, a.settings.DeviceID, a.settings.PersonID, a.settings.PersonName = "", "", "", ""
	st := a.settings
	a.client, a.overview, a.revoked, a.syncErr, a.overviewErr, a.lastSync = nil, nil, false, "", "", nil
	a.mu.Unlock()

	if err := settings.Save(st); err != nil {
		return err
	}
	a.changed()
	if deviceID == "" {
		return nil
	}
	return secret.DeleteToken(deviceID)
}
