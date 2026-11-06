// A throwaway Postgres schema per test file (or test), so tests of db(),
// kv and enqueue run against a real database without seeing each other's
// rows.

import { randomBytes } from "node:crypto";
import pg from "pg";
import { swapPool } from "../db.ts";

export interface TestDatabaseOptions {
  /** The Postgres to make the schema in. Default: `$TEST_DATABASE_URL`. */
  url?: string;
  /**
   * Point the SDK at the schema until `drop()`: `db()`, `kv` and `enqueue`
   * use it, and `DATABASE_URL` is its URL (for queue workers and child
   * processes). Default true.
   */
  use?: boolean;
}

export interface TestDatabase {
  /** The schema's name, `maxcloud_test_` and something unique. */
  readonly schema: string;
  /** A DATABASE_URL whose connections have `search_path` set to the schema. */
  readonly url: string;
  /** A pool on `url`; the one `db()` returns while it's in use. */
  readonly pool: pg.Pool;
  /** Closes the pool, drops the schema and everything in it, and puts back the SDK's previous database. */
  drop(): Promise<void>;
}

/** Schemas testDatabase() makes start with this. */
export const TEST_SCHEMA_PREFIX = "maxcloud_test_";

/**
 * Makes a uniquely named schema in `$TEST_DATABASE_URL` and points the SDK
 * at it:
 *
 * ```ts
 * import { after, before, test } from "node:test";
 * import { kv } from "@max-cloud/functions";
 * import { testDatabase, type TestDatabase } from "@max-cloud/functions/testing";
 *
 * let t: TestDatabase;
 * before(async () => { t = await testDatabase(); });
 * after(() => t.drop());
 *
 * test("kv", async () => {
 *   await kv.set("a", 1);
 *   assert.equal(await kv.get("a"), 1);
 * });
 * ```
 *
 * Unqualified table names resolve in the schema only (`public` isn't on
 * the search path), so the SDK's tables and your migrations land there.
 */
export async function testDatabase(options: TestDatabaseOptions = {}): Promise<TestDatabase> {
  const base = options.url ?? process.env.TEST_DATABASE_URL;
  if (!base) {
    throw new Error(
      "testDatabase(): TEST_DATABASE_URL isn't set. Point it at a Postgres you can create schemas in, e.g. " +
        "docker run -d --rm -p 127.0.0.1:5433:5432 -e POSTGRES_PASSWORD=test postgres:18 and " +
        "TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:5433/postgres",
    );
  }
  const schema = TEST_SCHEMA_PREFIX + Date.now().toString(36) + "_" + randomBytes(4).toString("hex");
  await admin(base, `CREATE SCHEMA ${schema}`);
  const url = withSearchPath(base, schema);
  const pool = new pg.Pool({ connectionString: url, max: 4 });

  const use = options.use ?? true;
  let prevPool: pg.Pool | undefined;
  let prevEnv: string | undefined;
  if (use) {
    prevPool = swapPool(pool);
    prevEnv = process.env.DATABASE_URL;
    process.env.DATABASE_URL = url;
  }

  let dropped: Promise<void> | undefined;
  return {
    schema,
    url,
    pool,
    drop() {
      dropped ??= (async () => {
        if (use) {
          // Put things back only if nobody has swapped them since.
          const current = swapPool(prevPool);
          if (current !== pool) swapPool(current);
          if (process.env.DATABASE_URL === url) {
            if (prevEnv === undefined) delete process.env.DATABASE_URL;
            else process.env.DATABASE_URL = prevEnv;
          }
        }
        await pool.end();
        await admin(base, `DROP SCHEMA IF EXISTS ${schema} CASCADE`);
      })();
      return dropped;
    },
  };
}

async function admin(url: string, sql: string): Promise<void> {
  const client = new pg.Client({ connectionString: url });
  await client.connect();
  try {
    await client.query(sql);
  } finally {
    await client.end();
  }
}

/** url with `-c search_path=schema` added to its startup options. */
export function withSearchPath(url: string, schema: string): string {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    throw new Error("testDatabase(): TEST_DATABASE_URL isn't a postgres:// URL");
  }
  const options = u.searchParams.get("options");
  u.searchParams.set("options", (options ? options + " " : "") + `-c search_path=${schema}`);
  return u.toString();
}
