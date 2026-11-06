// SQL migrations: the files in the app's migrations directory, applied
// once each, in name order, and recorded in maxcloud_migrations.
//
// mcloud db migrate and the Go SDK do the same (internal/migrate in the
// maxcloud repo); all of them are tested against the repo's
// testdata/migrate, so they never disagree about what's applied.

import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import pg from "pg";

/** Where an app keeps its migrations: next to package.json (and maxcloud.yaml). */
export const MIGRATIONS_DIR = "migrations";
/**
 * The table that records the applied migrations. Not schema-qualified: the
 * one the connection's search_path finds (public.maxcloud_migrations on a
 * default connection), and a new one goes in the connection's current
 * schema, next to the app's tables (a testDatabase() schema's own).
 */
export const MIGRATIONS_TABLE = "maxcloud_migrations";
/** The session advisory lock a run holds while it applies: the ASCII bytes of "maxcloud". */
export const MIGRATIONS_LOCK_KEY = "7881713191291417956";

const CREATE_TABLE = `CREATE TABLE IF NOT EXISTS ${MIGRATIONS_TABLE} (
  name text PRIMARY KEY,
  checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`;

export interface Migration {
  /** 0001_init.sql */
  name: string;
  /** Hex SHA-256 of the file's bytes. */
  checksum: string;
  sql: string;
}

/** applied; pending; changed (the file isn't what was applied); missing (applied, no file). */
export type MigrationState = "applied" | "pending" | "changed" | "missing";

export interface MigrationEntry {
  name: string;
  state: MigrationState;
  appliedAt?: Date;
}

const NAME = /^([0-9]+)_[A-Za-z0-9._-]+\.sql$/;

/**
 * Whether dir is a drizzle-kit migrations folder: its files are named like
 * maxcloud's (0000_name.sql), but drizzle applies and records them itself.
 */
export function isDrizzleKitDir(dir: string): boolean {
  return existsSync(join(dir, "meta", "_journal.json"));
}

/** The names of the migrations migrate() would apply from dir: none for a drizzle-kit folder. */
export function runtimeMigrations(dir: string): string[] {
  return isDrizzleKitDir(dir) ? [] : loadMigrations(dir).map((m) => m.name);
}

/**
 * Reads the migrations in `dir`: its files ending in .sql, in name order.
 * Other files and subdirectories are left alone; a missing directory is no
 * migrations. Every .sql file's name must be a number, an underscore and a
 * description (0001_init.sql), the numbers all of one width.
 */
export function loadMigrations(dir: string): Migration[] {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return [];
    throw err;
  }
  const names = entries
    .filter((e) => !e.isDirectory() && e.name.endsWith(".sql"))
    .map((e) => e.name)
    .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  const out: Migration[] = [];
  let width = 0;
  let widthFrom = "";
  for (const name of names) {
    const m = NAME.exec(name);
    if (!m) throw new Error(`${dir}/${name}: name a migration NUMBER_DESCRIPTION.sql, e.g. 0001_init.sql`);
    if (width === 0) {
      width = m[1]!.length;
      widthFrom = name;
    } else if (m[1]!.length !== width) {
      throw new Error(`${dir}/${widthFrom} and ${name}: give the numbers the same width (0009_, 0010_), so the files sort in the order they apply`);
    }
    const bytes = readFileSync(join(dir, name));
    out.push({ name, checksum: createHash("sha256").update(bytes).digest("hex"), sql: bytes.toString("utf8") });
  }
  return out;
}

interface Applied {
  checksum: string;
  at: Date;
}

async function tableExists(client: pg.ClientBase): Promise<boolean> {
  const { rows } = await client.query<{ exists: boolean }>("SELECT to_regclass($1) IS NOT NULL AS exists", [MIGRATIONS_TABLE]);
  return rows[0]?.exists ?? false;
}

async function applied(client: pg.ClientBase): Promise<Map<string, Applied>> {
  const out = new Map<string, Applied>();
  if (!(await tableExists(client))) return out;
  const res = await client.query<{ name: string; checksum: string; applied_at: Date }>(`SELECT name, checksum, applied_at FROM ${MIGRATIONS_TABLE}`);
  for (const r of res.rows) out.set(r.name, { checksum: r.checksum, at: r.applied_at });
  return out;
}

/** Compares `files` with what the database has applied. Changes nothing, takes no lock. */
export async function migrationStatus(client: pg.ClientBase, files: Migration[]): Promise<MigrationEntry[]> {
  const done = await applied(client);
  const out: MigrationEntry[] = [];
  for (const f of files) {
    const r = done.get(f.name);
    if (!r) out.push({ name: f.name, state: "pending" });
    else out.push({ name: f.name, state: r.checksum === f.checksum ? "applied" : "changed", appliedAt: r.at });
  }
  const seen = new Set(files.map((f) => f.name));
  for (const [name, r] of done) if (!seen.has(name)) out.push({ name, state: "missing", appliedAt: r.at });
  return out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

function check(files: Migration[], done: Map<string, Applied>) {
  for (const f of files) {
    const r = done.get(f.name);
    if (r && r.checksum !== f.checksum) {
      const at = r.at.toISOString().slice(0, 16).replace("T", " ") + " UTC";
      throw new Error(
        `${MIGRATIONS_DIR}/${f.name} changed after it was applied (${at}): put the change in a new migration instead. ` +
          `If the edit changes nothing the database needs (a comment, whitespace), record it: ` +
          `UPDATE ${MIGRATIONS_TABLE} SET checksum = '${f.checksum}' WHERE name = '${f.name}'`,
      );
    }
  }
}

/** Names the file, and the line when Postgres says where. */
function fileError(f: Migration, err: unknown): Error {
  const msg = err instanceof Error ? err.message : String(err);
  const pos = Number((err as { position?: string }).position ?? 0);
  if (pos > 0) {
    const before = Array.from(f.sql).slice(0, pos - 1).join("");
    const line = 1 + (before.match(/\n/g)?.length ?? 0);
    return new Error(`${MIGRATIONS_DIR}/${f.name}, line ${line}: ${msg}`, { cause: err });
  }
  return new Error(`${MIGRATIONS_DIR}/${f.name}: ${msg}`, { cause: err });
}

/**
 * Applies the pending migrations in name order, each in its own transaction
 * together with its row in the table, so one that fails leaves nothing
 * behind. Refuses to apply anything if an applied migration's file
 * changed; leaves alone migrations the database has but `files` doesn't.
 * Only one run applies at a time (an advisory lock); with nothing pending
 * it takes no lock and creates nothing. Resolves to the names it applied.
 */
export async function applyMigrations(client: pg.ClientBase, files: Migration[], log: (msg: string) => void = () => {}): Promise<string[]> {
  let done = await applied(client);
  check(files, done);
  if (files.every((f) => done.has(f.name))) return [];

  const { rows } = await client.query<{ locked: boolean }>("SELECT pg_try_advisory_lock($1::bigint) AS locked", [MIGRATIONS_LOCK_KEY]);
  if (!rows[0]?.locked) {
    log("waiting for another migration run to finish");
    await client.query("SELECT pg_advisory_lock($1::bigint)", [MIGRATIONS_LOCK_KEY]);
  }
  const names: string[] = [];
  try {
    // Only when the search_path finds none: CREATE TABLE IF NOT EXISTS would
    // make a second, empty one in an earlier schema (a "$user" schema made
    // after the app first migrated into public).
    if (!(await tableExists(client))) await client.query(CREATE_TABLE);
    // Another run may have applied some while this one waited.
    done = await applied(client);
    check(files, done);
    for (const f of files) {
      if (done.has(f.name)) continue;
      log(`applying ${f.name}`);
      await client.query("BEGIN");
      try {
        // No values: the simple protocol, so a file may hold many statements.
        await client.query(f.sql).catch((err) => {
          throw fileError(f, err);
        });
        await client.query(`INSERT INTO ${MIGRATIONS_TABLE} (name, checksum) VALUES ($1, $2)`, [f.name, f.checksum]);
        await client.query("COMMIT");
      } catch (err) {
        await client.query("ROLLBACK").catch(() => {});
        throw err;
      }
      names.push(f.name);
    }
  } finally {
    await client.query("SELECT pg_advisory_unlock($1::bigint)", [MIGRATIONS_LOCK_KEY]).catch(() => {});
  }
  return names;
}

export interface MigrateOptions {
  /** Default: DATABASE_URL. */
  url?: string;
  /** Default: migrations in the working directory. */
  dir?: string;
  log?: (msg: string) => void;
}

/**
 * Applies the app's pending migrations (see applyMigrations) on a
 * connection of its own. The runtime does this before it runs a function;
 * call it yourself for a script or a test. Resolves to the names applied.
 *
 * ```ts
 * import { migrate } from "@max-cloud/functions";
 * await migrate({ url: process.env.TEST_DATABASE_URL });
 * ```
 */
export async function migrate(opts: MigrateOptions = {}): Promise<string[]> {
  const dir = opts.dir ?? resolve(MIGRATIONS_DIR);
  if (isDrizzleKitDir(dir)) {
    opts.log?.(`${dir} is drizzle-kit's (meta/_journal.json): not applied here, it would apply a second time; run drizzle's migrator`);
    return [];
  }
  const files = loadMigrations(dir);
  if (files.length === 0) return [];
  const url = opts.url ?? process.env.DATABASE_URL;
  if (!url) throw new Error(`the app has ${files.length} migration(s), but DATABASE_URL isn't set (db: true in maxcloud.yaml)`);
  const client = new pg.Client({ connectionString: url });
  await client.connect();
  try {
    return await applyMigrations(client, files, opts.log);
  } finally {
    await client.end();
  }
}
