// The app's Postgres database (DATABASE_URL): one shared pool, which the
// queue and kv code use too.

import pg from "pg";

let pool: pg.Pool | undefined;

/** DATABASE_URL, or an error that says where it comes from. */
export function databaseUrl(): string {
  const url = process.env.DATABASE_URL;
  if (!url) {
    throw new Error(
      "DATABASE_URL isn't set: the app has no Postgres database (add db: true to its maxcloud.yaml), " +
        "or this isn't running on maxcloud (mcloud dev sets it locally)",
    );
  }
  return url;
}

/**
 * The app's database: a `pg.Pool` on DATABASE_URL, made on first use and
 * shared with `enqueue` and `kv`.
 *
 * ```ts
 * import { db } from "@max-cloud/functions";
 * const { rows } = await db().query("SELECT id, name FROM users WHERE id = $1", [id]);
 * ```
 *
 * Throws if DATABASE_URL isn't set.
 */
export function db(): pg.Pool {
  pool ??= new pg.Pool({ connectionString: databaseUrl(), max: 4 });
  return pool;
}

/**
 * Replaces the shared pool (undefined: make a new one from DATABASE_URL on
 * next use) and returns the one it replaces. For
 * `@max-cloud/functions/testing`; not part of the public API.
 */
export function swapPool(next: pg.Pool | undefined): pg.Pool | undefined {
  const prev = pool;
  pool = next;
  return prev;
}
