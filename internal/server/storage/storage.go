// Package storage is the server's Postgres database.
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/identity"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

//go:embed migrations/*.sql
var migrations embed.FS

var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidInvite = errors.New("invite is invalid, expired or already used")
	ErrUnauthorized  = errors.New("device token is invalid or revoked")
	ErrDuplicate     = errors.New("already exists")
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		db.Close()
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, dir)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := provider.Up(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Device is an authenticated device together with its owner.
type Device struct {
	identity.Device
	Person identity.Person
}

func (s *Store) AddPerson(ctx context.Context, name string) (identity.Person, error) {
	p := identity.Person{ID: newID(), DisplayName: name}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO persons (id, display_name) VALUES ($1, $2)`, p.ID, p.DisplayName)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on display_name
		return identity.Person{}, fmt.Errorf("person %q: %w", name, ErrDuplicate)
	}
	if err != nil {
		return identity.Person{}, fmt.Errorf("add person: %w", err)
	}
	return p, nil
}

func (s *Store) Persons(ctx context.Context) ([]identity.Person, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, display_name FROM persons ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.Person
	for rows.Next() {
		var p identity.Person
		if err := rows.Scan(&p.ID, &p.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Person(ctx context.Context, id string) (identity.Person, error) {
	var p identity.Person
	err := s.db.QueryRowContext(ctx,
		`SELECT id, display_name FROM persons WHERE id = $1`, id).
		Scan(&p.ID, &p.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Person{}, fmt.Errorf("person %q: %w", id, ErrNotFound)
	}
	return p, err
}

// InviteTTL is how long a join link stays valid.
const InviteTTL = 24 * time.Hour

// CreateInvite returns a one-time code; only its hash is stored.
func (s *Store) CreateInvite(ctx context.Context, personID string, ttl time.Duration) (string, time.Time, error) {
	code := newSecret()
	expires := time.Now().Add(ttl).UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invites (code_hash, person_id, expires_at) VALUES ($1, $2, $3)`, hashSecret(code), personID, expires)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create invite: %w", err)
	}
	return code, expires, nil
}

// Pair redeems an invite for a new device and returns its token, which is
// shown only this once.
func (s *Store) Pair(ctx context.Context, code, deviceName, platform string) (Device, string, error) {
	var d Device
	token := newSecret()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT p.id, p.display_name FROM invites i JOIN persons p ON p.id = i.person_id
			WHERE i.code_hash = $1 AND i.used_at IS NULL AND i.expires_at > now()
			FOR UPDATE OF i`, hashSecret(code)).Scan(&d.Person.ID, &d.Person.DisplayName)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidInvite
		}
		if err != nil {
			return err
		}
		d.Device = identity.Device{ID: newID(), PersonID: d.Person.ID, Name: deviceName, Platform: identity.Platform(platform)}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO devices (id, person_id, name, platform, token_hash) VALUES ($1, $2, $3, $4, $5)`,
			d.ID, d.PersonID, d.Name, d.Platform, hashSecret(token)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE invites SET used_at = now(), device_id = $1 WHERE code_hash = $2`, d.ID, hashSecret(code))
		return err
	})
	if err != nil {
		return Device{}, "", err
	}
	return d, token, nil
}

func (s *Store) DeviceByToken(ctx context.Context, token string) (Device, error) {
	var d Device
	var platform string
	err := s.db.QueryRowContext(ctx, `
		UPDATE devices SET last_seen_at = now()
		FROM persons p
		WHERE devices.token_hash = $1 AND devices.revoked_at IS NULL AND p.id = devices.person_id
		RETURNING devices.id, devices.name, devices.platform, p.id, p.display_name`, hashSecret(token)).
		Scan(&d.ID, &d.Name, &platform, &d.Person.ID, &d.Person.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrUnauthorized
	}
	if err != nil {
		return Device{}, err
	}
	d.PersonID = d.Person.ID
	d.Platform = identity.Platform(platform)
	return d, nil
}

type DeviceRow struct {
	identity.Device
	PersonName string
	LastSeenAt *time.Time
	RevokedAt  *time.Time
}

func (s *Store) Devices(ctx context.Context) ([]DeviceRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.person_id, p.display_name, d.name, d.platform, d.last_seen_at, d.revoked_at
		FROM devices d JOIN persons p ON p.id = d.person_id ORDER BY p.display_name, d.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRow
	for rows.Next() {
		var r DeviceRow
		var platform string
		if err := rows.Scan(&r.ID, &r.PersonID, &r.PersonName, &r.Name, &platform, &r.LastSeenAt, &r.RevokedAt); err != nil {
			return nil, err
		}
		r.Platform = identity.Platform(platform)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("active device %q: %w", id, ErrNotFound)
	}
	return nil
}

// Ingest stores one sync batch atomically. Accounts and memberships are
// created on first sight, so a member joins an account simply by using it.
// Re-sent events are ignored unless they carry a larger output count.
func (s *Store) Ingest(ctx context.Context, d Device, req syncapi.SyncRequest) (int, error) {
	accepted := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		accounts := map[[2]string]string{}
		ensure := func(provider, refHash, hint, plan string) (string, error) {
			key := [2]string{provider, refHash}
			if id, ok := accounts[key]; ok && hint == "" {
				return id, nil
			}
			id, err := ensureAccount(ctx, tx, provider, refHash, hint, plan, d.PersonID)
			if err != nil {
				return "", err
			}
			accounts[key] = id
			return id, nil
		}

		for _, o := range req.Observations {
			if o.AccountRefHash == "" {
				continue
			}
			id, err := ensure(o.Provider, o.AccountRefHash, o.Hint, o.PlanType)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO observations (device_id, account_id, observed_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				d.ID, id, o.ObservedAt)
			if err != nil {
				return fmt.Errorf("save observation: %w", err)
			}
			accepted += rowsAffected(res)
		}

		for _, e := range req.Events {
			if e.AccountRefHash == "" || e.DedupeKey == "" {
				continue
			}
			id, err := ensure(e.Provider, e.AccountRefHash, "", "")
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO usage_events (dedupe_key, account_id, person_id, device_id, product, originator, session_id,
					model, occurred_at, input, cached_input, cache_write, output, reasoning_output)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				ON CONFLICT (dedupe_key) DO UPDATE SET
					output = excluded.output, reasoning_output = excluded.reasoning_output
				WHERE excluded.output > usage_events.output`,
				e.DedupeKey, id, d.PersonID, d.ID, e.Product, e.Originator, e.SessionID, e.Model, e.OccurredAt,
				e.Input, e.CachedInput, e.CacheWrite, e.Output, e.ReasoningOutput)
			if err != nil {
				return fmt.Errorf("save event %s: %w", e.DedupeKey, err)
			}
			accepted += rowsAffected(res)
		}

		for _, snap := range req.Snapshots {
			if snap.AccountRefHash == "" || len(snap.Buckets) == 0 {
				continue
			}
			id, err := ensure(snap.Provider, snap.AccountRefHash, "", "")
			if err != nil {
				return err
			}
			buckets, err := json.Marshal(snap.Buckets)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO quota_snapshots (account_id, device_id, source, observed_at, buckets)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
				id, d.ID, snap.Source, snap.ObservedAt, buckets)
			if err != nil {
				return fmt.Errorf("save snapshot: %w", err)
			}
			accepted += rowsAffected(res)
		}
		return nil
	})
	return accepted, err
}

func ensureAccount(ctx context.Context, tx *sql.Tx, provider, refHash, hint, plan, personID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO accounts (id, provider, ref_hash, hint, label, plan_type) VALUES ($1, $2, $3, $4, $4, $5)
		ON CONFLICT (provider, ref_hash) DO UPDATE SET
			hint = COALESCE(NULLIF(excluded.hint, ''), accounts.hint),
			label = CASE WHEN accounts.label = '' THEN excluded.label ELSE accounts.label END,
			plan_type = COALESCE(NULLIF(excluded.plan_type, ''), accounts.plan_type)
		RETURNING id`, newID(), provider, refHash, hint, plan).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("ensure account: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (account_id, person_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, personID); err != nil {
		return "", fmt.Errorf("ensure membership: %w", err)
	}
	return id, nil
}

func (s *Store) Accounts(ctx context.Context) ([]account.Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, provider, ref_hash, hint, label, plan_type FROM accounts ORDER BY provider, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []account.Account
	for rows.Next() {
		var a account.Account
		var provider string
		if err := rows.Scan(&a.ID, &provider, &a.ExternalRefHash, &a.Hint, &a.Label, &a.PlanType); err != nil {
			return nil, err
		}
		a.Provider = account.Provider(provider)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Account(ctx context.Context, id string) (account.Account, error) {
	var a account.Account
	var provider string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, provider, ref_hash, hint, label, plan_type FROM accounts WHERE id = $1`, id).
		Scan(&a.ID, &provider, &a.ExternalRefHash, &a.Hint, &a.Label, &a.PlanType)
	if errors.Is(err, sql.ErrNoRows) {
		return account.Account{}, fmt.Errorf("account %q: %w", id, ErrNotFound)
	}
	a.Provider = account.Provider(provider)
	return a, err
}

func (s *Store) SetAccountLabel(ctx context.Context, id, label string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET label = $2 WHERE id = $1`, id, label)
	return err
}

func (s *Store) SetShareWeight(ctx context.Context, accountID, personID string, weight float64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memberships (account_id, person_id, share_weight) VALUES ($1, $2, $3)
		ON CONFLICT (account_id, person_id) DO UPDATE SET share_weight = excluded.share_weight`,
		accountID, personID, weight)
	return err
}

type Member struct {
	PersonID    string
	Name        string
	ShareWeight float64
}

func (s *Store) Members(ctx context.Context, accountID string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.display_name, m.share_weight FROM memberships m JOIN persons p ON p.id = m.person_id
		WHERE m.account_id = $1 ORDER BY p.display_name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.PersonID, &m.Name, &m.ShareWeight); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Snapshots(ctx context.Context, accountID string, since time.Time) ([]quota.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source, observed_at, buckets FROM quota_snapshots
		WHERE account_id = $1 AND observed_at >= $2`, accountID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quota.Snapshot
	for rows.Next() {
		var dto syncapi.Snapshot
		var buckets []byte
		if err := rows.Scan(&dto.Source, &dto.ObservedAt, &buckets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(buckets, &dto.Buckets); err != nil {
			return nil, fmt.Errorf("decode stored buckets: %w", err)
		}
		out = append(out, dto.ToDomain())
	}
	return out, rows.Err()
}

// UsageRow is one person's token totals on one model.
type UsageRow struct {
	PersonID string
	Model    string
	Tokens   usage.Tokens
	Requests int
}

func (s *Store) Usage(ctx context.Context, accountID string, from, to time.Time) ([]UsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT person_id, model, sum(input), sum(cached_input), sum(cache_write), sum(output), sum(reasoning_output), count(*)
		FROM usage_events WHERE account_id = $1 AND occurred_at >= $2 AND occurred_at <= $3
		GROUP BY person_id, model`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		t := &r.Tokens
		if err := rows.Scan(&r.PersonID, &r.Model, &t.Input, &t.CachedInput, &t.CacheWrite, &t.Output, &t.ReasoningOutput, &r.Requests); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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

func rowsAffected(res sql.Result) int {
	n, _ := res.RowsAffected()
	return int(n)
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newSecret is a 160-bit random value, lower-case base32 so it survives
// being pasted from chat apps and URLs.
func newSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
