package agent

import (
	"context"
	"errors"
	"fmt"
)

// ResyncStats reports what a rebuilt ledger holds.
type ResyncStats struct {
	// Events is how many usage events the rescan attributed.
	Events int
	// Pending counts outbox items the server has not accepted yet.
	Pending int
}

// Resync rebuilds this device's ledger from its local logs, so events
// uploaded under older attribution rules are replaced by a fresh reading of
// the same files. Observations stay: they date the join, and the rescan needs
// the sign-in timeline to attribute usage.
func (a *Agent) Resync(ctx context.Context) (ResyncStats, error) {
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()
	if client == nil {
		return ResyncStats{}, errors.New("not joined; run `sharecodex join LINK` first")
	}
	if err := a.store.ResetLedger(ctx); err != nil {
		return ResyncStats{}, err
	}
	stats := ResyncStats{}

	a.observeAll(ctx)
	known, err := a.store.KnownFiles(ctx)
	if err != nil {
		return stats, err
	}
	for _, src := range sources {
		if _, err := a.scanSource(ctx, src, known); err != nil {
			return stats, fmt.Errorf("rescan %s: %w", src.provider, err)
		}
	}
	// The statusLine spool is attributed through the events just rebuilt.
	if _, err := a.ingestStatusLine(ctx); err != nil {
		return stats, fmt.Errorf("rescan statusLine: %w", err)
	}
	if stats.Events, err = a.store.EventCount(ctx); err != nil {
		return stats, err
	}
	if err := a.upload(ctx, client); err != nil {
		return stats, fmt.Errorf("upload rebuilt events: %w", err)
	}
	if stats.Pending, err = a.store.OutboxLen(ctx); err != nil {
		return stats, err
	}
	// Leaves the popup's quota view current even though nothing else ran.
	if err := a.syncOnce(ctx); err != nil {
		return stats, err
	}
	return stats, nil
}
