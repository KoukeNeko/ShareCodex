// Package storage is the desktop client's local SQLite database: the files
// already scanned, the account timeline, the local copy of the ledger, and
// the outbox of changes not yet accepted by the server.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
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
func (s *Store) IngestFile(ctx context.Context, path string, st scan.FileState, events []usage.Event, snapshots []quota.Snapshot) (int, error) {
	changed := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO events (dedupe_key, account_ref_hash, provider, product, originator, session_id, model,
					occurred_at, input, cached_input, cache_write, output, reasoning_output)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (dedupe_key) DO UPDATE SET
					output = excluded.output, reasoning_output = excluded.reasoning_output
				WHERE excluded.output > events.output`,
				e.DedupeKey, e.AccountRefHash, e.Provider, e.Product, e.Originator, e.SessionID, e.Model,
				e.OccurredAt.UnixMilli(), e.Tokens.Input, e.Tokens.CachedInput, e.Tokens.CacheWrite,
				e.Tokens.Output, e.Tokens.ReasoningOutput)
			if err != nil {
				return fmt.Errorf("save event %s: %w", e.DedupeKey, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			changed++
			if err := enqueue(ctx, tx, KindEvent, syncapi.FromEvent(e)); err != nil {
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

func insertSnapshot(ctx context.Context, tx *sql.Tx, snap quota.Snapshot) (int, error) {
	dto := syncapi.FromSnapshot(snap)
	buckets, err := json.Marshal(dto.Buckets)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO snapshots (provider, account_ref_hash, source, observed_at, buckets) VALUES (?, ?, ?, ?, ?)`,
		snap.Provider, snap.AccountRefHash, snap.Source, snap.ObservedAt.UnixMilli(), string(buckets))
	if err != nil {
		return 0, fmt.Errorf("save snapshot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, nil
	}
	return 1, enqueue(ctx, tx, KindSnapshot, dto)
}

// LatestSnapshots returns the snapshots of each account, newest first, for
// showing local quota while the server is unreachable.
func (s *Store) LatestSnapshots(ctx context.Context, since time.Time) ([]quota.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, account_ref_hash, source, observed_at, buckets FROM snapshots
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
		if err := rows.Scan(&dto.Provider, &dto.AccountRefHash, &dto.Source, &at, &buckets); err != nil {
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
