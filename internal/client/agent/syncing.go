package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
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
// failure resends them; the server ignores duplicates.
func (a *Agent) syncOnce(ctx context.Context) error {
	a.mu.Lock()
	client, revoked := a.client, a.revoked
	a.mu.Unlock()
	if client == nil || revoked {
		return nil
	}

	err := a.upload(ctx, client)
	if err == nil {
		var o syncapi.Overview
		if o, err = client.Overview(ctx); err == nil {
			a.mu.Lock()
			a.overview = &o
			a.mu.Unlock()
		}
	}

	now := time.Now()
	a.mu.Lock()
	switch {
	case errors.Is(err, sync.ErrRevoked):
		a.revoked = true
		a.syncErr = err.Error()
	case err != nil:
		a.syncErr = err.Error()
	default:
		a.syncErr = ""
		a.lastSync = &now
	}
	a.mu.Unlock()
	if err != nil {
		a.log.Warn("sync failed", "err", err)
	}
	a.changed()
	return err
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
		if _, err := client.Sync(ctx, req); err != nil {
			return err
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
	a.revoked, a.syncErr, a.overview = false, "", nil
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

// Leave forgets the server on this device. Usage already uploaded stays on
// the server; an admin revokes the device there.
func (a *Agent) Leave() error {
	a.mu.Lock()
	deviceID := a.settings.DeviceID
	a.settings.ServerURL, a.settings.DeviceID, a.settings.PersonID, a.settings.PersonName = "", "", "", ""
	st := a.settings
	a.client, a.overview, a.revoked, a.syncErr, a.lastSync = nil, nil, false, "", nil
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
