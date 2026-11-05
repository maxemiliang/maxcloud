// Key-value storage, like Workers KV but in the app's own Postgres database
// (DATABASE_URL): one table, maxcloud_kv. Reads see writes right away (no
// eventual consistency, but no global replicas either). Values are JSON.
// Expired keys read as missing and are cleaned up now and then by writes.

import pg from "pg";
import { defaultPool, type Queryable } from "./queue.ts";

export const KV_TABLE = "maxcloud_kv";

const SCHEMA = `
CREATE TABLE IF NOT EXISTS ${KV_TABLE} (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  expires_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ${KV_TABLE}_expires ON ${KV_TABLE} (expires_at) WHERE expires_at IS NOT NULL;
`;

const live = "(expires_at IS NULL OR expires_at > now())";

export interface KVSetOptions {
  /** Forget the key after this many seconds. */
  ttlSeconds?: number;
  /** Write in your own transaction (the table must exist already). */
  client?: Queryable;
}

export interface KVListOptions {
  /** Only keys starting with this. */
  prefix?: string;
  /** At most this many keys (default 100, max 1000). */
  limit?: number;
  /** Continue after a previous list's cursor. */
  cursor?: string;
}

export interface KVList {
  keys: string[];
  /** Pass to the next list() for more; undefined when there are no more. */
  cursor?: string;
}

const ready = new WeakMap<object, Promise<void>>();

function ensure(db: pg.Pool): Promise<void> {
  let p = ready.get(db);
  if (!p) {
    p = (async () => {
      const client = await db.connect();
      try {
        await client.query("BEGIN");
        await client.query(`SELECT pg_advisory_xact_lock(hashtext('${KV_TABLE}'))`);
        await client.query(SCHEMA);
        await client.query("COMMIT");
      } catch (err) {
        await client.query("ROLLBACK").catch(() => {});
        throw err;
      } finally {
        client.release();
      }
    })();
    p.catch(() => ready.delete(db));
    ready.set(db, p);
  }
  return p;
}

async function pool(): Promise<pg.Pool> {
  const db = defaultPool();
  await ensure(db);
  return db;
}

function checkKey(key: string): void {
  if (typeof key !== "string" || key.length === 0 || key.length > 512) {
    throw new Error(`kv: key must be a string of 1 to 512 characters, got ${JSON.stringify(key)}`);
  }
}

/**
 * The app's key-value store.
 *
 * ```ts
 * import { kv } from "@max-cloud/functions";
 * await kv.set("session:42", { user: "ann" }, { ttlSeconds: 3600 });
 * const s = await kv.get<{ user: string }>("session:42");
 * ```
 */
export const kv = {
  /** The value, or undefined if the key isn't there (or expired). */
  async get<T = unknown>(key: string): Promise<T | undefined> {
    checkKey(key);
    const db = await pool();
    const r = await db.query(`SELECT value FROM ${KV_TABLE} WHERE key = $1 AND ${live}`, [key]);
    return r.rows[0]?.value as T | undefined;
  },

  /** Stores value (anything JSON can hold) under key, replacing what was there. */
  async set<T>(key: string, value: T, options: KVSetOptions = {}): Promise<void> {
    checkKey(key);
    if (value === undefined) throw new Error("kv.set: value is undefined (use kv.delete)");
    const ttl = options.ttlSeconds;
    if (ttl !== undefined && !(ttl > 0)) throw new Error(`kv.set: ttlSeconds must be positive, got ${ttl}`);
    const db = options.client ?? (await pool());
    await db.query(
      `INSERT INTO ${KV_TABLE} (key, value, expires_at, updated_at)
       VALUES ($1, $2, CASE WHEN $3::float8 IS NULL THEN NULL ELSE now() + make_interval(secs => $3::float8) END, now())
       ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at, updated_at = now()`,
      [key, JSON.stringify(value), ttl ?? null],
    );
    // Now and then, clear out expired keys.
    if (Math.random() < 0.01) {
      await db.query(`DELETE FROM ${KV_TABLE} WHERE expires_at <= now()`);
    }
  },

  /** Removes key. True if it was there. */
  async delete(key: string, options: { client?: Queryable } = {}): Promise<boolean> {
    checkKey(key);
    const db = options.client ?? (await pool());
    const r = await db.query(`DELETE FROM ${KV_TABLE} WHERE key = $1 AND ${live}`, [key]);
    return (r.rowCount ?? 0) > 0;
  },

  /** Keys in order, optionally under a prefix, a page at a time. */
  async list(options: KVListOptions = {}): Promise<KVList> {
    const limit = Math.min(Math.max(options.limit ?? 100, 1), 1000);
    const db = await pool();
    const r = await db.query(
      `SELECT key FROM ${KV_TABLE}
       WHERE starts_with(key, $1) AND key > $2 AND ${live}
       ORDER BY key LIMIT $3`,
      [options.prefix ?? "", options.cursor ?? "", limit + 1],
    );
    const keys = r.rows.slice(0, limit).map((row) => row.key as string);
    return { keys, cursor: r.rows.length > limit ? keys[keys.length - 1] : undefined };
  },
};
