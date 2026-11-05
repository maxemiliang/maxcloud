package functions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Key-value storage, like Workers KV but in the app's own Postgres database
// ($DATABASE_URL): one table, maxcloud_kv. Reads see writes right away.
// Values are JSON. Expired keys read as missing and are cleaned up now and
// then by writes.

const kvTable = "maxcloud_kv"

const kvSchema = `
CREATE TABLE IF NOT EXISTS ` + kvTable + ` (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  expires_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ` + kvTable + `_expires ON ` + kvTable + ` (expires_at) WHERE expires_at IS NOT NULL;
`

const kvLive = "(expires_at IS NULL OR expires_at > now())"

var kvReady sync.Map // *pgxpool.Pool -> struct{}

func kvPool(ctx context.Context) (*pgxpool.Pool, error) {
	p, err := Pool(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := kvReady.Load(p); ok {
		return p, nil
	}
	err = pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('"+kvTable+"'))"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, kvSchema)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("functions: kv table: %w", err)
	}
	kvReady.Store(p, struct{}{})
	return p, nil
}

func checkKey(key string) error {
	if key == "" || len(key) > 512 {
		return fmt.Errorf("functions: kv key must be 1 to 512 bytes, got %q", key)
	}
	return nil
}

// KVGet reads key into v (as JSON). It reports false, and leaves v alone,
// if the key isn't there or has expired.
func KVGet(ctx context.Context, key string, v any) (bool, error) {
	if err := checkKey(key); err != nil {
		return false, err
	}
	p, err := kvPool(ctx)
	if err != nil {
		return false, err
	}
	var raw []byte
	err = p.QueryRow(ctx, "SELECT value FROM "+kvTable+" WHERE key = $1 AND "+kvLive, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, v)
}

// KVSet stores v (as JSON) under key, replacing what was there. ttl > 0
// forgets it after ttl; 0 keeps it.
func KVSet(ctx context.Context, key string, v any, ttl time.Duration) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("functions: kv ttl must not be negative, got %s", ttl)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p, err := kvPool(ctx)
	if err != nil {
		return err
	}
	var secs any
	if ttl > 0 {
		secs = ttl.Seconds()
	}
	_, err = p.Exec(ctx, `INSERT INTO `+kvTable+` (key, value, expires_at, updated_at)
		VALUES ($1, $2, CASE WHEN $3::float8 IS NULL THEN NULL ELSE now() + make_interval(secs => $3::float8) END, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at, updated_at = now()`,
		key, raw, secs)
	if err != nil {
		return err
	}
	// Now and then, clear out expired keys.
	if rand.IntN(100) == 0 {
		_, err = p.Exec(ctx, "DELETE FROM "+kvTable+" WHERE expires_at <= now()")
	}
	return err
}

// KVDelete removes key and reports whether it was there.
func KVDelete(ctx context.Context, key string) (bool, error) {
	if err := checkKey(key); err != nil {
		return false, err
	}
	p, err := kvPool(ctx)
	if err != nil {
		return false, err
	}
	tag, err := p.Exec(ctx, "DELETE FROM "+kvTable+" WHERE key = $1 AND "+kvLive, key)
	return tag.RowsAffected() > 0, err
}

// KVList returns up to limit keys (default 100, max 1000) in order that
// start with prefix and come after cursor (""), and the cursor for the next
// page ("" when there are no more).
func KVList(ctx context.Context, prefix, cursor string, limit int) (keys []string, next string, err error) {
	if limit <= 0 || limit > 1000 {
		limit = min(max(limit, 100), 1000)
	}
	p, err := kvPool(ctx)
	if err != nil {
		return nil, "", err
	}
	rows, err := p.Query(ctx, `SELECT key FROM `+kvTable+`
		WHERE starts_with(key, $1) AND key > $2 AND `+kvLive+`
		ORDER BY key LIMIT $3`, prefix, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, "", err
	}
	if len(keys) > limit {
		keys = keys[:limit]
		next = keys[limit-1]
	}
	return keys, next, nil
}
