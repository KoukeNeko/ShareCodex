// Package storage is the desktop client's local SQLite database: the files
// already scanned, the account timeline, the local copy of the ledger, and
// the outbox of changes not yet accepted by the server.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/scan"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

//go:embed migrations/*.sql
var migrations embed.FS

const (
	KindObservation = "observation"
	KindEvent       = "event"
	KindSnapshot    = "snapshot"
	KindLimitEvent  = "limit_event"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	// A single connection serialises writers; the agent is the only writer
	// and SQLite would otherwise return SQLITE_BUSY under concurrent writes.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open local db: %w", err)
	}
	db.SetMaxOpenConns(1)

	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		db.Close()
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, dir)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate local db: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) AttributionReviewed(ctx context.Context) (bool, error) {
	var reviewed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM attribution_review)`).Scan(&reviewed)
	return reviewed, err
}

func (s *Store) MarkAttributionReviewed(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO attribution_review (id) VALUES (1)`)
	return err
}

func (s *Store) KnownFiles(ctx context.Context) (map[string]scan.FileState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, size, mod_time_ns FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make(map[string]scan.FileState)
	for rows.Next() {
		var path string
		var size, modNS int64
		if err := rows.Scan(&path, &size, &modNS); err != nil {
			return nil, err
		}
		files[path] = scan.FileState{Size: size, ModTime: time.Unix(0, modNS)}
	}
	return files, rows.Err()
}

// AddObservation records which account the device is logged into. Only
// observations of a pooled account are queued for the server.
func (s *Store) AddObservation(ctx context.Context, o account.Observation) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO observations (provider, source, ref_hash, hint, plan_type, observed_at) VALUES (?, ?, ?, ?, ?, ?)`,
			o.Provider, o.Source, o.ExternalRefHash, o.Hint, o.PlanType, o.ObservedAt.UnixMilli())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 || o.ExternalRefHash == "" {
			return nil
		}
		return enqueue(ctx, tx, KindObservation, syncapi.FromObservation(o))
	})
}

// Observations returns one app's account timeline for a provider, oldest
// first.
func (s *Store) Observations(ctx context.Context, provider account.Provider, source account.Source) ([]account.Observation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ref_hash, hint, plan_type, observed_at FROM observations WHERE provider = ? AND source = ? ORDER BY observed_at`,
		provider, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []account.Observation
	for rows.Next() {
		o := account.Observation{Provider: provider, Source: source}
		var at int64
		if err := rows.Scan(&o.ExternalRefHash, &o.Hint, &o.PlanType, &at); err != nil {
			return nil, err
		}
		o.ObservedAt = time.UnixMilli(at)
		out = append(out, o)
	}
	return out, rows.Err()
}

// IngestFile stores what was parsed from one file and marks the file as
// scanned, atomically, so a crash never leaves a file marked but unsaved.
// Events are upserted: a response re-read later with a larger output count
// (Claude streams partial counts) replaces the smaller one and is re-queued.
func (s *Store) IngestFile(ctx context.Context, path string, st scan.FileState, events []usage.Event, snapshots []quota.Snapshot, limits []usage.LimitEvent) (int, error) {
	changed := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			var previousRef, previousSession, previousOriginator string
			err := tx.QueryRowContext(ctx, `SELECT account_ref_hash, session_id, originator FROM events WHERE dedupe_key = ?`, e.DedupeKey).
				Scan(&previousRef, &previousSession, &previousOriginator)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO events (dedupe_key, account_ref_hash, provider, product, originator, session_id, model,
					occurred_at, input, cached_input, cache_write, output, reasoning_output, third_party, gateway,
					request_id, parent_request_id, effort, status, aggregated)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (dedupe_key) DO UPDATE SET
					account_ref_hash = excluded.account_ref_hash, output = max(events.output, excluded.output),
					reasoning_output = max(events.reasoning_output, excluded.reasoning_output),
					request_id = CASE WHEN events.request_id = '' THEN excluded.request_id ELSE events.request_id END,
					parent_request_id = CASE WHEN events.parent_request_id = '' THEN excluded.parent_request_id ELSE events.parent_request_id END,
					effort = CASE WHEN events.effort = '' THEN excluded.effort ELSE events.effort END,
					status = CASE WHEN events.status = '' THEN excluded.status ELSE events.status END,
					aggregated = events.aggregated OR excluded.aggregated
				WHERE (excluded.account_ref_hash = events.account_ref_hash AND (excluded.output > events.output OR (events.request_id = '' AND excluded.request_id != '') OR
						(events.effort = '' AND excluded.effort != ''))) OR
					(excluded.account_ref_hash != events.account_ref_hash AND
					 excluded.session_id = events.session_id AND excluded.originator = events.originator)`,
				e.DedupeKey, e.AccountRefHash, e.Provider, e.Product, e.Originator, e.SessionID, e.Model,
				e.OccurredAt.UnixMilli(), e.Tokens.Input, e.Tokens.CachedInput, e.Tokens.CacheWrite,
				e.Tokens.Output, e.Tokens.ReasoningOutput, e.ThirdParty, e.Gateway,
				e.RequestID, e.ParentRequestID, e.Effort, e.Status, e.Aggregated)
			if err != nil {
				return fmt.Errorf("save event %s: %w", e.DedupeKey, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			changed++
			payload := syncapi.FromEvent(e)
			if previousRef != "" && previousRef != e.AccountRefHash && previousSession == e.SessionID && previousOriginator == e.Originator {
				payload.PreviousAccountRefHash = previousRef
			}
			if err := enqueue(ctx, tx, KindEvent, payload); err != nil {
				return err
			}
		}
		for _, snap := range snapshots {
			n, err := insertSnapshot(ctx, tx, snap)
			if err != nil {
				return err
			}
			changed += n
		}
		for _, l := range limits {
			n, err := insertLimitEvent(ctx, tx, l)
			if err != nil {
				return err
			}
			changed += n
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO files (path, size, mod_time_ns) VALUES (?, ?, ?)
			 ON CONFLICT (path) DO UPDATE SET size = excluded.size, mod_time_ns = excluded.mod_time_ns`,
			path, st.Size, st.ModTime.UnixNano())
		return err
	})
	return changed, err
}

// AddSnapshot stores a quota snapshot that did not come from a log file.
func (s *Store) AddSnapshot(ctx context.Context, snap quota.Snapshot) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := insertSnapshot(ctx, tx, snap)
		return err
	})
}

// CorrectSnapshot reattributes a recorded statusLine reading. The old row
// must have the same provider-reported buckets as the session's spool file.
func (s *Store) CorrectSnapshot(ctx context.Context, snap quota.Snapshot) (bool, error) {
	corrected := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		buckets, err := json.Marshal(syncapi.FromSnapshot(snap).Buckets)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT account_ref_hash FROM snapshots
			WHERE provider = ? AND source = ? AND observed_at = ? AND buckets = ?`,
			snap.Provider, snap.Source, snap.ObservedAt.UnixMilli(), string(buckets))
		if err != nil {
			return err
		}
		var previousRefs []string
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				rows.Close()
				return err
			}
			previousRefs = append(previousRefs, ref)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(previousRefs) != 1 || previousRefs[0] == snap.AccountRefHash {
			return nil
		}
		previousRef := previousRefs[0]
		res, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE provider = ? AND account_ref_hash = ? AND source = ? AND observed_at = ?`,
			snap.Provider, previousRef, snap.Source, snap.ObservedAt.UnixMilli())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		payload := syncapi.FromSnapshot(snap)
		payload.PreviousAccountRefHash = previousRef
		if err := enqueue(ctx, tx, KindSnapshot, payload); err != nil {
			return err
		}
		if _, err := insertSnapshot(ctx, tx, snap); err != nil {
			return err
		}
		corrected = true
		return nil
	})
	return corrected, err
}

func insertSnapshot(ctx context.Context, tx *sql.Tx, snap quota.Snapshot) (int, error) {
	dto := syncapi.FromSnapshot(snap)
	buckets, err := json.Marshal(dto.Buckets)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO snapshots (provider, account_ref_hash, source, observed_at, buckets, plan_type, account_hint) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		snap.Provider, snap.AccountRefHash, snap.Source, snap.ObservedAt.UnixMilli(), string(buckets), snap.PlanType, snap.AccountHint)
	if err != nil {
		return 0, fmt.Errorf("save snapshot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, nil
	}
	return 1, enqueue(ctx, tx, KindSnapshot, dto)
}

// SessionOriginatorAt returns the entrypoint of the latest recorded event in
// a session at or before the snapshot, or "" when none is recorded.
func (s *Store) SessionOriginatorAt(ctx context.Context, sessionID string, at time.Time) (string, error) {
	var originator string
	err := s.db.QueryRowContext(ctx,
		`SELECT originator FROM events WHERE provider = ? AND session_id = ? AND occurred_at <= ?
		 ORDER BY occurred_at DESC LIMIT 1`, account.ProviderAnthropic, sessionID, at.UnixMilli()).Scan(&originator)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return originator, err
}

// LastUse returns the account and time of a provider's latest recorded event
// from any of the given entrypoints, or a zero time when there is none. A
// third-party request says nothing about the Claude account in use.
func (s *Store) LastUse(ctx context.Context, provider account.Provider, originators []string) (string, time.Time, error) {
	if len(originators) == 0 {
		return "", time.Time{}, nil
	}
	args := []any{provider}
	for _, o := range originators {
		args = append(args, o)
	}
	var ref string
	var at int64
	err := s.db.QueryRowContext(ctx,
		`SELECT account_ref_hash, occurred_at FROM events
		 WHERE provider = ? AND third_party = 0 AND originator IN (?`+strings.Repeat(", ?", len(originators)-1)+`)
		 ORDER BY occurred_at DESC LIMIT 1`, args...).Scan(&ref, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return ref, time.UnixMilli(at), nil
}

// LatestSnapshots returns the snapshots of each account, newest first, for
// showing local quota while the server is unreachable.
func (s *Store) LatestSnapshots(ctx context.Context, since time.Time) ([]quota.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, account_ref_hash, source, observed_at, buckets, plan_type, account_hint FROM snapshots
		 WHERE observed_at >= ? ORDER BY observed_at DESC`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quota.Snapshot
	for rows.Next() {
		var dto syncapi.Snapshot
		var at int64
		var buckets string
		if err := rows.Scan(&dto.Provider, &dto.AccountRefHash, &dto.Source, &at, &buckets, &dto.PlanType, &dto.AccountHint); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(buckets), &dto.Buckets); err != nil {
			return nil, fmt.Errorf("decode stored buckets: %w", err)
		}
		dto.ObservedAt = time.UnixMilli(at)
		out = append(out, dto.ToDomain())
	}
	return out, rows.Err()
}

type OutboxItem struct {
	ID      int64
	Kind    string
	Payload json.RawMessage
}

func (s *Store) PendingOutbox(ctx context.Context, limit int) ([]OutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, payload FROM outbox ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		var payload string
		if err := rows.Scan(&it.ID, &it.Kind, &payload); err != nil {
			return nil, err
		}
		it.Payload = json.RawMessage(payload)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) OutboxLen(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox`).Scan(&n)
	return n, err
}

// DeleteOutboxThrough removes items up to and including id after the server
// accepted them; items are always sent in id order.
func (s *Store) DeleteOutboxThrough(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id <= ?`, id)
	return err
}

// ResetLedger drops everything this device reported as usage and nothing
// else: observations stay, because they date the join and hold the sign-in
// timeline a rescan attributes events with. The server's copy may have been
// reset too, so the timeline and quota readings are queued again; the
// server ignores the ones it still has.
func (s *Store) ResetLedger(ctx context.Context) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"events", "files", "outbox", "limit_events"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
				return fmt.Errorf("clear %s: %w", table, err)
			}
		}
		if err := requeueObservations(ctx, tx); err != nil {
			return err
		}
		return requeueSnapshots(ctx, tx)
	})
}

func requeueObservations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT provider, source, ref_hash, hint, plan_type, observed_at FROM observations
		WHERE ref_hash != '' ORDER BY observed_at`)
	if err != nil {
		return err
	}
	var obs []account.Observation
	for rows.Next() {
		var o account.Observation
		var at int64
		if err := rows.Scan(&o.Provider, &o.Source, &o.ExternalRefHash, &o.Hint, &o.PlanType, &at); err != nil {
			rows.Close()
			return err
		}
		o.ObservedAt = time.UnixMilli(at)
		obs = append(obs, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range obs {
		if err := enqueue(ctx, tx, KindObservation, syncapi.FromObservation(o)); err != nil {
			return err
		}
	}
	return nil
}

func requeueSnapshots(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT provider, account_ref_hash, source, observed_at, buckets, plan_type, account_hint FROM snapshots ORDER BY observed_at`)
	if err != nil {
		return err
	}
	var snaps []syncapi.Snapshot
	for rows.Next() {
		var dto syncapi.Snapshot
		var at int64
		var buckets string
		if err := rows.Scan(&dto.Provider, &dto.AccountRefHash, &dto.Source, &at, &buckets, &dto.PlanType, &dto.AccountHint); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(buckets), &dto.Buckets); err != nil {
			rows.Close()
			return fmt.Errorf("decode stored buckets: %w", err)
		}
		dto.ObservedAt = time.UnixMilli(at).UTC()
		snaps = append(snaps, dto)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, snap := range snaps {
		if err := enqueue(ctx, tx, KindSnapshot, snap); err != nil {
			return err
		}
	}
	return nil
}

func insertLimitEvent(ctx context.Context, tx *sql.Tx, l usage.LimitEvent) (int, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO limit_events (dedupe_key, provider, account_ref_hash, occurred_at, observed_at,
			session_id, request_id, kind, source, evidence, http_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.DedupeKey, l.Provider, l.AccountRefHash, l.OccurredAt.UnixMilli(), l.ObservedAt.UnixMilli(),
		l.SessionID, l.RequestID, string(l.Kind), l.Source, l.Evidence, l.HTTPStatus)
	if err != nil {
		return 0, fmt.Errorf("save limit event: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, nil
	}
	return 1, enqueue(ctx, tx, KindLimitEvent, syncapi.FromLimitEvent(l))
}

func (s *Store) EventCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n)
	return n, err
}

// ClaudeLogin is a Claude account signed in to ShareCodex itself.
type ClaudeLogin struct {
	RefHash  string
	Hint     string
	PlanType string
}

// AddClaudeLogin records a sign-in, replacing an earlier one of the account.
func (s *Store) AddClaudeLogin(ctx context.Context, l ClaudeLogin) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO claude_logins (ref_hash, hint, plan_type) VALUES (?, ?, ?)
		 ON CONFLICT (ref_hash) DO UPDATE SET hint = excluded.hint, plan_type = excluded.plan_type`,
		l.RefHash, l.Hint, l.PlanType)
	return err
}

func (s *Store) ClaudeLogins(ctx context.Context) ([]ClaudeLogin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ref_hash, hint, plan_type FROM claude_logins ORDER BY hint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaudeLogin
	for rows.Next() {
		var l ClaudeLogin
		if err := rows.Scan(&l.RefHash, &l.Hint, &l.PlanType); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) RemoveClaudeLogin(ctx context.Context, refHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM claude_logins WHERE ref_hash = ?`, refHash)
	return err
}

func enqueue(ctx context.Context, tx *sql.Tx, kind string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox (kind, payload) VALUES (?, ?)`, kind, string(b))
	return err
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
