package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/keys"
)

// keyRotationLockKey is the advisory lock key serializing key rotation across replicas.
const keyRotationLockKey int64 = 0x706c617466726f74 // "platfrot"

const selectKeys = `SELECT kid, public_jwk, private_enc, kek_id, state, created_at, activated_at, retired_at, removed_at
	FROM signing_keys WHERE state <> 'removed' ORDER BY created_at, kid`

// KeyStore implements keys.Store on PostgreSQL.
type KeyStore struct {
	pool  *pgxpool.Pool
	clock func() time.Time // nil in production: the database clock decides
}

// KeyStoreOption configures a KeyStore.
type KeyStoreOption func(*KeyStore)

// WithClock replaces the database clock as the source of Tx.Now. It exists only so tests can drive the
// rotation lifecycle with a fake clock; production code must not use it, because rotation timestamps
// must come from one place (PostgreSQL) for every replica.
func WithClock(now func() time.Time) KeyStoreOption {
	return func(s *KeyStore) { s.clock = now }
}

// NewKeyStore returns a KeyStore over pool.
func NewKeyStore(pool *pgxpool.Pool, opts ...KeyStoreOption) *KeyStore {
	s := &KeyStore{pool: pool}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ keys.Store = (*KeyStore)(nil)

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listKeys(ctx context.Context, q queryer) ([]keys.Record, error) {
	rows, err := q.Query(ctx, selectKeys)
	if err != nil {
		return nil, fmt.Errorf("query signing_keys: %w", err)
	}
	defer rows.Close()
	var out []keys.Record
	for rows.Next() {
		var r keys.Record
		if err := rows.Scan(&r.KID, &r.JWK, &r.PrivateEnc, &r.KEKID, &r.State, &r.CreatedAt, &r.ActivatedAt, &r.RetiredAt, &r.RemovedAt); err != nil {
			return nil, fmt.Errorf("scan signing_keys: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadAll returns every non-removed key.
func (s *KeyStore) LoadAll(ctx context.Context) ([]keys.Record, error) {
	return listKeys(ctx, s.pool)
}

// Locked implements keys.Store.
func (s *KeyStore) Locked(ctx context.Context, wait bool, fn func(keys.Tx) error) (acquired bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if !acquired || err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if wait {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", keyRotationLockKey); err != nil {
			return false, fmt.Errorf("advisory lock: %w", err)
		}
	} else {
		var got bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", keyRotationLockKey).Scan(&got); err != nil {
			return false, fmt.Errorf("try advisory lock: %w", err)
		}
		if !got {
			return false, nil
		}
	}
	if err := fn(keyTx{tx: tx, clock: s.clock}); err != nil {
		return true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

type keyTx struct {
	tx    pgx.Tx
	clock func() time.Time
}

// Now returns the database clock. clock_timestamp() rather than now(): the transaction may have waited
// for the advisory lock, and now() would report the moment the transaction began, before the wait.
func (t keyTx) Now(ctx context.Context) (time.Time, error) {
	if t.clock != nil {
		return t.clock(), nil
	}
	var now time.Time
	if err := t.tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("database time: %w", err)
	}
	return now, nil
}

func (t keyTx) List(ctx context.Context) ([]keys.Record, error) { return listKeys(ctx, t.tx) }

func (t keyTx) Insert(ctx context.Context, r keys.Record) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO signing_keys (kid, public_jwk, private_enc, kek_id, state, created_at, activated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, r.KID, r.JWK, r.PrivateEnc, r.KEKID, r.State, r.CreatedAt, r.ActivatedAt)
	if err != nil {
		return fmt.Errorf("insert signing key: %w", err)
	}
	return nil
}

func (t keyTx) SetState(ctx context.Context, kid string, to keys.State, at time.Time) error {
	var sql string
	switch to {
	case keys.StateActive:
		sql = "UPDATE signing_keys SET state = 'active', activated_at = $2 WHERE kid = $1 AND state <> 'removed'"
	case keys.StateRetired:
		sql = "UPDATE signing_keys SET state = 'retired', retired_at = $2 WHERE kid = $1 AND state <> 'removed'"
	case keys.StateRemoved:
		sql = "UPDATE signing_keys SET state = 'removed', removed_at = $2, private_enc = NULL WHERE kid = $1 AND state <> 'removed'"
	default:
		return fmt.Errorf("unsupported target state %q", to)
	}
	tag, err := t.tx.Exec(ctx, sql, kid, at)
	if err != nil {
		return fmt.Errorf("update signing key: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return keys.ErrKeyNotFound
	}
	return nil
}

// Subscribe implements keys.Store with a dedicated LISTEN connection (see listenOnce).
func (s *KeyStore) Subscribe(ctx context.Context, notify func()) error {
	return listenOnce(ctx, s.pool, keys.NotifyChannel, notify)
}
