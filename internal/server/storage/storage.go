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
	"strconv"
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
	ErrNotFound            = errors.New("not found")
	ErrInvalidInvite       = errors.New("invite is invalid, expired or already used")
	ErrUnauthorized        = errors.New("device token is invalid or revoked")
	ErrDuplicate           = errors.New("already exists")
	ErrInvalidPlanInterval = errors.New("invalid plan interval")
	ErrPlanIntervalOverlap = errors.New("plan interval overlaps with existing interval")
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

// DeletePerson removes a member with their devices, join links and
// memberships, and everything their devices uploaded: usage, quota readings
// and sign-ins. Their devices can no longer sync.
func (s *Store) DeletePerson(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		const devices = `SELECT id FROM devices WHERE person_id = $1`
		for _, q := range []string{
			`DELETE FROM usage_events WHERE person_id = $1 OR device_id IN (` + devices + `)`,
			`DELETE FROM quota_snapshots WHERE device_id IN (` + devices + `)`,
			`DELETE FROM observations WHERE device_id IN (` + devices + `)`,
			`DELETE FROM invites WHERE person_id = $1 OR device_id IN (` + devices + `)`,
			`DELETE FROM memberships WHERE person_id = $1`,
			`DELETE FROM devices WHERE person_id = $1`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return fmt.Errorf("delete person's records: %w", err)
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM persons WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if rowsAffected(res) == 0 {
			return fmt.Errorf("person %q: %w", id, ErrNotFound)
		}
		return nil
	})
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

// Ingest stores one sync batch atomically and reports what the ledger holds
// afterwards. Accounts and memberships are created on first sight, so a member
// joins an account simply by using it.
//
// Re-sent events are ignored unless they carry a larger output count or an
// account correction. Any device of the person the record belongs to may
// correct, as long as it names the account the record was previously
// attributed to: one request can appear in more than one of a person's
// devices' logs (Claude Desktop mirrors a session it drives on another host),
// and the device that uploaded it first may be the one that knows it least.
// Another person's device never changes a record, since the dedupe key is
// global and a record is only ever that person's own usage. The person a
// record belongs to never changes, because a device only reports events whose
// account it observed itself.
func (s *Store) Ingest(ctx context.Context, d Device, req syncapi.SyncRequest) (syncapi.SyncResponse, error) {
	accepted := 0
	diverged := 0
	// Events this batch attributes, for the divergence count below.
	var reported []syncapi.Event
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		accounts := map[[2]string]string{}
		// planAt is when plan was observed; zero when plan is empty.
		ensure := func(provider, refHash, hint, plan string, planAt time.Time) (string, error) {
			key := [2]string{provider, refHash}
			if id, ok := accounts[key]; ok && hint == "" && plan == "" {
				return id, nil
			}
			id, err := ensureAccount(ctx, tx, provider, refHash, hint, plan, planAt, d.PersonID)
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
			// claude auth status reports the plan stored with Claude Code's
			// sign-in, which an upgrade does not change; Claude's plan comes
			// from usage readings instead, which ask Anthropic.
			plan := o.PlanType
			if o.Provider == string(account.ProviderAnthropic) {
				plan = ""
			}
			id, err := ensure(o.Provider, o.AccountRefHash, o.Hint, plan, o.ObservedAt)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO observations (device_id, account_id, source, observed_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
				d.ID, id, o.Source, o.ObservedAt)
			if err != nil {
				return fmt.Errorf("save observation: %w", err)
			}
			accepted += rowsAffected(res)
		}

		for _, e := range req.Events {
			if e.AccountRefHash == "" || e.DedupeKey == "" {
				continue
			}
			id, err := ensure(e.Provider, e.AccountRefHash, "", "", time.Time{})
			if err != nil {
				return err
			}
			reported = append(reported, e)
			res, err := tx.ExecContext(ctx, `
				INSERT INTO usage_events (dedupe_key, account_id, person_id, device_id, product, originator, session_id,
					model, occurred_at, input, cached_input, cache_write, output, reasoning_output, third_party, gateway,
					request_id, parent_request_id, effort, status, aggregated)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $17, $18, $19, $20, $21, $22, $23)
				ON CONFLICT (dedupe_key) DO UPDATE SET
					account_id = excluded.account_id,
					output = GREATEST(usage_events.output, excluded.output),
					reasoning_output = GREATEST(usage_events.reasoning_output, excluded.reasoning_output),
					request_id = CASE WHEN usage_events.request_id = '' THEN excluded.request_id ELSE usage_events.request_id END,
					parent_request_id = CASE WHEN usage_events.parent_request_id = '' THEN excluded.parent_request_id ELSE usage_events.parent_request_id END,
					effort = CASE WHEN usage_events.effort = '' THEN excluded.effort ELSE usage_events.effort END,
					status = CASE WHEN usage_events.status = '' THEN excluded.status ELSE usage_events.status END,
					aggregated = usage_events.aggregated OR excluded.aggregated
				WHERE usage_events.person_id = excluded.person_id AND
					((usage_events.account_id = excluded.account_id AND (excluded.output > usage_events.output OR (usage_events.request_id = '' AND excluded.request_id != '') OR
						(usage_events.effort = '' AND excluded.effort != ''))) OR
					 (usage_events.session_id = excluded.session_id AND usage_events.originator = excluded.originator AND
					  usage_events.account_id != excluded.account_id AND $16 != '' AND
					  usage_events.account_id = (SELECT id FROM accounts WHERE provider = $15 AND ref_hash = $16)))`,
				e.DedupeKey, id, d.PersonID, d.ID, e.Product, e.Originator, e.SessionID, e.Model, e.OccurredAt,
				e.Input, e.CachedInput, e.CacheWrite, e.Output, e.ReasoningOutput, e.Provider, e.PreviousAccountRefHash, e.ThirdParty,
				e.Gateway, e.RequestID, e.ParentRequestID, e.Effort, e.Status, e.Aggregated)
			if err != nil {
				return fmt.Errorf("save event %s: %w", e.DedupeKey, err)
			}
			accepted += rowsAffected(res)
			// A request uploaded as third-party can turn out to belong with
			// another provider's account, such as Claude Code using ChatGPT
			// through OpenCodex: counted there when it drew on that
			// subscription, third-party there when OpenCodex sent it to an
			// account outside the pool. Its own device's resync moves it.
			if e.Gateway != "" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE usage_events SET account_id = $2, model = $3, gateway = $4, third_party = $6
					WHERE dedupe_key = $1 AND device_id = $5 AND third_party AND account_id != $2`,
					e.DedupeKey, id, e.Model, e.Gateway, d.ID, e.ThirdParty); err != nil {
					return fmt.Errorf("move %s: %w", e.DedupeKey, err)
				}
			}
			// A resync marks requests uploaded before clients could tell
			// they were third-party, and names their gateway; a request never
			// goes back to counting against the quota, and a named gateway
			// stays.
			if e.ThirdParty || e.Gateway != "" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE usage_events SET third_party = third_party OR $2,
						gateway = CASE WHEN gateway = '' THEN $3 ELSE gateway END
					WHERE dedupe_key = $1 AND person_id = $4 AND (third_party != (third_party OR $2) OR (gateway = '' AND $3 != ''))`,
					e.DedupeKey, e.ThirdParty, e.Gateway, d.PersonID); err != nil {
					return fmt.Errorf("reclassify %s: %w", e.DedupeKey, err)
				}
			}
		}

		if len(reported) > 0 {
			keys := make([]string, len(reported))
			providers := make([]string, len(reported))
			refs := make([]string, len(reported))
			for i, e := range reported {
				keys[i], providers[i], refs[i] = e.DedupeKey, e.Provider, e.AccountRefHash
			}
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FROM unnest($1::text[], $2::text[], $3::text[]) AS sent(dedupe_key, provider, ref_hash)
				JOIN usage_events e ON e.dedupe_key = sent.dedupe_key
				JOIN accounts a ON a.id = e.account_id
				WHERE a.provider <> sent.provider OR a.ref_hash <> sent.ref_hash`,
				keys, providers, refs).Scan(&diverged); err != nil {
				return fmt.Errorf("count divergent events: %w", err)
			}
		}

		for _, snap := range req.Snapshots {
			if snap.AccountRefHash == "" || len(snap.Buckets) == 0 {
				continue
			}
			id, err := ensure(snap.Provider, snap.AccountRefHash, snap.AccountHint, snap.PlanType, snap.ObservedAt)
			if err != nil {
				return err
			}
			buckets, err := json.Marshal(snap.Buckets)
			if err != nil {
				return err
			}
			if snap.PreviousAccountRefHash != "" && snap.PreviousAccountRefHash != snap.AccountRefHash {
				res, err := tx.ExecContext(ctx, `DELETE FROM quota_snapshots
					WHERE account_id = (SELECT id FROM accounts WHERE provider = $1 AND ref_hash = $2)
					AND device_id = $3 AND source = $4 AND observed_at = $5 AND buckets = $6`,
					snap.Provider, snap.PreviousAccountRefHash, d.ID, snap.Source, snap.ObservedAt, buckets)
				if err != nil {
					return fmt.Errorf("correct snapshot: %w", err)
				}
				accepted += rowsAffected(res)
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO quota_snapshots (account_id, device_id, source, observed_at, buckets, plan_type)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (account_id, device_id, source, observed_at) DO UPDATE SET
					plan_type = excluded.plan_type
				WHERE quota_snapshots.plan_type = '' AND excluded.plan_type != ''`,
				id, d.ID, snap.Source, snap.ObservedAt, buckets, snap.PlanType)
			if err != nil {
				return fmt.Errorf("save snapshot: %w", err)
			}
			accepted += rowsAffected(res)
		}

		for _, l := range req.LimitEvents {
			if l.AccountRefHash == "" || l.DedupeKey == "" {
				continue
			}
			id, err := ensure(l.Provider, l.AccountRefHash, "", "", time.Time{})
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO limit_events (dedupe_key, account_id, person_id, device_id, provider,
					occurred_at, observed_at, session_id, request_id, kind, source, evidence, http_status)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
				ON CONFLICT (dedupe_key) DO NOTHING`,
				l.DedupeKey, id, d.PersonID, d.ID, l.Provider,
				l.OccurredAt, l.ObservedAt, l.SessionID, l.RequestID, l.Kind, l.Source, l.Evidence, l.HTTPStatus)
			if err != nil {
				return fmt.Errorf("save limit event %s: %w", l.DedupeKey, err)
			}
			accepted += rowsAffected(res)
		}
		return nil
	})
	return syncapi.SyncResponse{Accepted: accepted, Diverged: diverged}, err
}

func ensureAccount(ctx context.Context, tx *sql.Tx, provider, refHash, hint, plan string, planAt time.Time, personID string) (string, error) {
	var at sql.NullTime
	if plan != "" {
		at = sql.NullTime{Time: planAt, Valid: true}
	}
	var id string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO accounts (id, provider, ref_hash, hint, label, plan_type, plan_observed_at) VALUES ($1, $2, $3, $4, $4, $5, $6)
		ON CONFLICT (provider, ref_hash) DO UPDATE SET
			hint = COALESCE(NULLIF(excluded.hint, ''), accounts.hint),
			label = CASE WHEN accounts.label = '' THEN excluded.label ELSE accounts.label END,
			-- A resync replays old sign-ins, so only a newer reading changes
			-- the plan. claude auth status reports "max" without the tier a
			-- usage reading carries ("max 20x"); the plain plan never
			-- replaces it.
			plan_type = CASE
				WHEN excluded.plan_type = '' OR excluded.plan_observed_at < accounts.plan_observed_at OR
					accounts.plan_type LIKE excluded.plan_type || ' %' THEN accounts.plan_type
				ELSE excluded.plan_type END,
			plan_observed_at = GREATEST(accounts.plan_observed_at, excluded.plan_observed_at)
		RETURNING id`, newID(), provider, refHash, hint, plan, at).Scan(&id)
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
		`SELECT id, provider, ref_hash, hint, label, plan_type FROM accounts ORDER BY position NULLS LAST, provider, label`)
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

// SetAccountOrder arranges accounts in the order of ids; accounts not
// listed keep no position and follow them.
func (s *Store) SetAccountOrder(ctx context.Context, ids []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET position = NULL`); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE accounts SET position = $2 WHERE id = $1`, id, i); err != nil {
				return err
			}
		}
		return nil
	})
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

// DeleteAccount removes an account with everything recorded on it: its
// usage, quota readings, sign-ins and memberships. A device still signed
// into it creates it again on its next sync.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"usage_events", "quota_snapshots", "observations", "memberships"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE account_id = $1`, id); err != nil {
				return fmt.Errorf("delete account's %s: %w", table, err)
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if rowsAffected(res) == 0 {
			return fmt.Errorf("account %q: %w", id, ErrNotFound)
		}
		return nil
	})
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
		SELECT source, observed_at, buckets, plan_type FROM quota_snapshots
		WHERE account_id = $1 AND observed_at >= $2`, accountID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quota.Snapshot
	for rows.Next() {
		var dto syncapi.Snapshot
		var buckets []byte
		if err := rows.Scan(&dto.Source, &dto.ObservedAt, &buckets, &dto.PlanType); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(buckets, &dto.Buckets); err != nil {
			return nil, fmt.Errorf("decode stored buckets: %w", err)
		}
		out = append(out, dto.ToDomain())
	}
	return out, rows.Err()
}

// ActiveDevice is the account a device's CLI was last signed into for one
// provider.
type ActiveDevice struct {
	AccountID  string
	DeviceID   string
	PersonID   string
	PersonName string
	DeviceName string
}

// ActiveDevices returns, for each device that is not revoked, the account of
// its latest observation per provider and app (the CLI or Claude Desktop),
// if that observation is after since. A CLI that signs out stops sending
// observations, and Claude Desktop's are dated by its last activity, so
// either drops out once its last observation is older than since.
func (s *Store) ActiveDevices(ctx context.Context, since time.Time) ([]ActiveDevice, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id, device_id, person_id, display_name, name FROM (
			SELECT DISTINCT ON (o.device_id, a.provider, o.source) o.account_id, o.device_id, d.person_id, p.display_name, d.name, o.observed_at
			FROM observations o
			JOIN devices d ON d.id = o.device_id
			JOIN persons p ON p.id = d.person_id
			JOIN accounts a ON a.id = o.account_id
			WHERE d.revoked_at IS NULL AND o.observed_at >= $1
			ORDER BY o.device_id, a.provider, o.source, o.observed_at DESC
		) latest`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveDevice
	for rows.Next() {
		var d ActiveDevice
		if err := rows.Scan(&d.AccountID, &d.DeviceID, &d.PersonID, &d.PersonName, &d.DeviceName); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UsageRow is one person's token totals on one model from one device.
type UsageRow struct {
	PersonID   string
	DeviceName string
	Model      string
	// Gateway keeps a model reached through a gateway, such as ChatGPT
	// from Claude Code through OpenCodex, apart from the same model used
	// directly.
	Gateway  string
	Tokens   usage.Tokens
	Requests int
}

func (s *Store) Usage(ctx context.Context, accountID string, from, to time.Time) ([]UsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.person_id, d.name, e.model, e.gateway, sum(e.input), sum(e.cached_input), sum(e.cache_write), sum(e.output),
			sum(e.reasoning_output), count(*)
		FROM usage_events e JOIN devices d ON d.id = e.device_id
		WHERE e.account_id = $1 AND e.occurred_at >= $2 AND e.occurred_at <= $3 AND NOT e.third_party
		GROUP BY e.person_id, d.name, e.model, e.gateway`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		t := &r.Tokens
		if err := rows.Scan(&r.PersonID, &r.DeviceName, &r.Model, &r.Gateway, &t.Input, &t.CachedInput, &t.CacheWrite, &t.Output, &t.ReasoningOutput, &r.Requests); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TimelineRow is one person's tokens on one model within one bin.
type TimelineRow struct {
	Bin        int
	PersonID   string
	Model      string
	ThirdParty bool
	Gateway    string
	Tokens     int64
}

// UsageTimeline sums an account's tokens per bin of binMinutes from start,
// for events before end.
func (s *Store) UsageTimeline(ctx context.Context, accountID string, start, end time.Time, binMinutes int) ([]TimelineRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT floor(extract(epoch FROM occurred_at - $2) / ($4 * 60))::int AS bin, person_id, model, third_party, gateway,
			sum(input + cached_input + cache_write + output)
		FROM usage_events WHERE account_id = $1 AND occurred_at >= $2 AND occurred_at < $3
		GROUP BY bin, person_id, model, third_party, gateway`, accountID, start, end, binMinutes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelineRow
	for rows.Next() {
		var r TimelineRow
		if err := rows.Scan(&r.Bin, &r.PersonID, &r.Model, &r.ThirdParty, &r.Gateway, &r.Tokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ThirdPartyRow is the tokens of one third-party model through one gateway.
type ThirdPartyRow struct {
	Model    string
	Gateway  string
	Tokens   usage.Tokens
	Requests int
}

// ThirdPartyUsage sums an account's third-party requests per model and
// gateway within [from, to]; they are shown apart from the quota.
func (s *Store) ThirdPartyUsage(ctx context.Context, accountID string, from, to time.Time) ([]ThirdPartyRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, gateway, sum(input), sum(cached_input), sum(cache_write), sum(output), sum(reasoning_output), count(*)
		FROM usage_events WHERE account_id = $1 AND occurred_at >= $2 AND occurred_at <= $3 AND third_party
		GROUP BY model, gateway`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThirdPartyRow
	for rows.Next() {
		var r ThirdPartyRow
		t := &r.Tokens
		if err := rows.Scan(&r.Model, &r.Gateway, &t.Input, &t.CachedInput, &t.CacheWrite, &t.Output, &t.ReasoningOutput, &r.Requests); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PersonUsage is one person's token totals on one model, across accounts.
type PersonUsage struct {
	Model    string
	Tokens   usage.Tokens
	Requests int
}

func (s *Store) PersonUsage(ctx context.Context, personID string, since time.Time) ([]PersonUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, sum(input), sum(cached_input), sum(cache_write), sum(output), sum(reasoning_output), count(*)
		FROM usage_events WHERE person_id = $1 AND occurred_at >= $2 AND NOT third_party
		GROUP BY model`, personID, since)
	if err != nil {
		return nil, err
	}
	return scanPersonUsage(rows)
}

func scanPersonUsage(rows *sql.Rows) ([]PersonUsage, error) {
	defer rows.Close()
	var out []PersonUsage
	for rows.Next() {
		var r PersonUsage
		t := &r.Tokens
		if err := rows.Scan(&r.Model, &t.Input, &t.CachedInput, &t.CacheWrite, &t.Output, &t.ReasoningOutput, &r.Requests); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SummaryRow is the totals of one person's requests from one device to one
// model on one account.
type SummaryRow struct {
	PersonID   string
	PersonName string
	DeviceID   string
	AccountID  string
	Model      string
	Gateway    string
	ThirdParty bool
	Tokens     usage.Tokens
	Requests   int
}

// UsageSummary sums every request since a time, for the usage reports. A
// person ID limits it to that member's requests.
func (s *Store) UsageSummary(ctx context.Context, since time.Time, personID string) ([]SummaryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.person_id, p.display_name, e.device_id, e.account_id, e.model, e.gateway, e.third_party,
			sum(e.input), sum(e.cached_input), sum(e.cache_write), sum(e.output), sum(e.reasoning_output), count(*)
		FROM usage_events e JOIN persons p ON p.id = e.person_id
		WHERE e.occurred_at >= $1 AND ($2 = '' OR e.person_id = $2)
		GROUP BY e.person_id, p.display_name, e.device_id, e.account_id, e.model, e.gateway, e.third_party`, since, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SummaryRow
	for rows.Next() {
		var r SummaryRow
		t := &r.Tokens
		if err := rows.Scan(&r.PersonID, &r.PersonName, &r.DeviceID, &r.AccountID, &r.Model, &r.Gateway, &r.ThirdParty,
			&t.Input, &t.CachedInput, &t.CacheWrite, &t.Output, &t.ReasoningOutput, &r.Requests); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const settingPublicDashboard = "public_dashboard"

// PublicDashboard reports whether the dashboard is published without
// sign-in. It is off until an admin turns it on.
func (s *Store) PublicDashboard(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, settingPublicDashboard).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "true", err
}

func (s *Store) SetPublicDashboard(ctx context.Context, on bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, settingPublicDashboard, strconv.FormatBool(on))
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

// PlanHistory returns all recorded plan intervals for an account, chronological by effective_at.
func (s *Store) PlanHistory(ctx context.Context, accountID string) ([]account.PlanInterval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, account_id, plan_type, effective_at, ended_at, reason, source, precision, created_at
		FROM plan_intervals
		WHERE account_id = $1
		ORDER BY effective_at ASC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []account.PlanInterval
	for rows.Next() {
		var in account.PlanInterval
		var endedAt sql.NullTime
		if err := rows.Scan(&in.ID, &in.AccountID, &in.PlanType, &in.EffectiveAt, &endedAt,
			&in.Reason, &in.Source, &in.Precision, &in.CreatedAt); err != nil {
			return nil, err
		}
		if endedAt.Valid {
			in.EndedAt = &endedAt.Time
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// SavePlanInterval inserts or updates a plan interval, validating that it does not
// overlap with other intervals for the same account.
func (s *Store) SavePlanInterval(ctx context.Context, in account.PlanInterval) (account.PlanInterval, error) {
	in.PlanType = strings.TrimSpace(in.PlanType)
	if in.AccountID == "" || in.PlanType == "" {
		return account.PlanInterval{}, ErrInvalidPlanInterval
	}
	if in.EndedAt != nil && !in.EndedAt.After(in.EffectiveAt) {
		return account.PlanInterval{}, ErrInvalidPlanInterval
	}
	if in.Precision == "" {
		in.Precision = account.PrecisionConfirmed
	}

	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var dummy string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, in.AccountID).Scan(&dummy); err != nil {
			return err
		}

		rows, err := tx.QueryContext(ctx, `
			SELECT id, effective_at, ended_at FROM plan_intervals WHERE account_id = $1 AND ($2 = '' OR id != $2)`,
			in.AccountID, in.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var eff time.Time
			var end sql.NullTime
			if err := rows.Scan(&id, &eff, &end); err != nil {
				return err
			}
			aStart := in.EffectiveAt
			aEnd := in.EndedAt
			bStart := eff
			var bEnd *time.Time
			if end.Valid {
				bEnd = &end.Time
			}

			aBeforeBEnd := bEnd == nil || aStart.Before(*bEnd)
			bBeforeAEnd := aEnd == nil || bStart.Before(*aEnd)
			if aBeforeBEnd && bBeforeAEnd {
				return ErrPlanIntervalOverlap
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}

		var endedAt sql.NullTime
		if in.EndedAt != nil {
			endedAt = sql.NullTime{Time: *in.EndedAt, Valid: true}
		}

		if in.ID == "" {
			in.ID = newID()
			return tx.QueryRowContext(ctx, `
				INSERT INTO plan_intervals (id, account_id, plan_type, effective_at, ended_at, reason, source, precision)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				RETURNING created_at`,
				in.ID, in.AccountID, in.PlanType, in.EffectiveAt, endedAt, in.Reason, in.Source, in.Precision).Scan(&in.CreatedAt)
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE plan_intervals
			SET plan_type = $2, effective_at = $3, ended_at = $4, reason = $5, source = $6, precision = $7
			WHERE id = $1 AND account_id = $8`,
			in.ID, in.PlanType, in.EffectiveAt, endedAt, in.Reason, in.Source, in.Precision, in.AccountID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return tx.QueryRowContext(ctx, `SELECT created_at FROM plan_intervals WHERE id = $1`, in.ID).Scan(&in.CreatedAt)
	})
	return in, err
}

func (s *Store) DeletePlanInterval(ctx context.Context, accountID, intervalID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM plan_intervals WHERE id = $1 AND account_id = $2`, intervalID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// HistoricalSnapshots returns an account's quota snapshots between start and end, chronological.
func (s *Store) HistoricalSnapshots(ctx context.Context, accountID string, start, end time.Time) ([]quota.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source, observed_at, buckets, plan_type FROM quota_snapshots
		WHERE account_id = $1 AND observed_at >= $2 AND observed_at < $3
		ORDER BY observed_at ASC`, accountID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quota.Snapshot
	for rows.Next() {
		var dto syncapi.Snapshot
		var buckets []byte
		if err := rows.Scan(&dto.Source, &dto.ObservedAt, &buckets, &dto.PlanType); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(buckets, &dto.Buckets); err != nil {
			return nil, fmt.Errorf("decode stored buckets: %w", err)
		}
		out = append(out, dto.ToDomain())
	}
	return out, rows.Err()
}

type StoredLimitEvent struct {
	usage.LimitEvent
	PersonName string
	DeviceName string
}

// HistoricalLimitEvents returns limit and refusal events for an account between start and end.
func (s *Store) HistoricalLimitEvents(ctx context.Context, accountID string, start, end time.Time) ([]StoredLimitEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.dedupe_key, l.account_id, l.person_id, p.display_name, l.device_id, d.name,
			l.provider, l.occurred_at, l.observed_at, l.session_id, l.request_id, l.kind,
			l.source, l.evidence, l.http_status
		FROM limit_events l
		JOIN persons p ON p.id = l.person_id
		JOIN devices d ON d.id = l.device_id
		WHERE l.account_id = $1 AND l.occurred_at >= $2 AND l.occurred_at < $3
		ORDER BY l.occurred_at ASC`, accountID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredLimitEvent
	for rows.Next() {
		var l StoredLimitEvent
		var kind string
		if err := rows.Scan(&l.DedupeKey, &l.AccountRefHash, &l.PersonID, &l.PersonName, &l.DeviceID, &l.DeviceName,
			&l.Provider, &l.OccurredAt, &l.ObservedAt, &l.SessionID, &l.RequestID, &kind,
			&l.Source, &l.Evidence, &l.HTTPStatus); err != nil {
			return nil, err
		}
		l.Kind = usage.LimitKind(kind)
		out = append(out, l)
	}
	return out, rows.Err()
}

// DemandEvent is a recorded request with its timestamp, person, model, and tokens.
type DemandEvent struct {
	OccurredAt time.Time
	PersonID   string
	DeviceID   string
	Model      string
	Gateway    string
	ThirdParty bool
	Tokens     usage.Tokens
}

// AccountDemandEvents returns requests on an account between start and end, chronological,
// for rolling demand analysis.
func (s *Store) AccountDemandEvents(ctx context.Context, accountID string, start, end time.Time) ([]DemandEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT occurred_at, person_id, device_id, model, gateway, third_party,
			input, cached_input, cache_write, output, reasoning_output
		FROM usage_events
		WHERE account_id = $1 AND occurred_at >= $2 AND occurred_at < $3
		ORDER BY occurred_at ASC`, accountID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DemandEvent
	for rows.Next() {
		var d DemandEvent
		var t = &d.Tokens
		if err := rows.Scan(&d.OccurredAt, &d.PersonID, &d.DeviceID, &d.Model, &d.Gateway, &d.ThirdParty,
			&t.Input, &t.CachedInput, &t.CacheWrite, &t.Output, &t.ReasoningOutput); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
