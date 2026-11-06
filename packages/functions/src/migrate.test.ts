// The cases are the repo's testdata/migrate, shared with mcloud and the Go
// SDK. The database tests need a Postgres:
// TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { cpSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomBytes } from "node:crypto";
import { fileURLToPath } from "node:url";
import pg from "pg";
import { MIGRATIONS_LOCK_KEY, MIGRATIONS_TABLE, applyMigrations, loadMigrations, migrationStatus } from "./migrate.ts";

const root = fileURLToPath(new URL("../../../testdata/migrate/", import.meta.url));
const cases = JSON.parse(readFileSync(join(root, "cases.json"), "utf8")) as {
  table: string;
  lockKey: string;
  load: { dir: string; files?: { name: string; checksum: string }[]; error?: string }[];
  apply: { dir: string; statusBefore?: { name: string; state: string }[]; applied?: string[]; error?: string; tablesAbsent?: string[] }[];
};

const url = process.env.TEST_DATABASE_URL;
const skip = url ? false : "set TEST_DATABASE_URL to run the migration tests";

test("the table and lock are the ones in cases.json", () => {
  assert.equal(MIGRATIONS_TABLE, cases.table);
  assert.equal(MIGRATIONS_LOCK_KEY, cases.lockKey);
  assert.equal(BigInt(MIGRATIONS_LOCK_KEY), BigInt("0x" + Buffer.from("maxcloud").toString("hex")));
});

for (const c of cases.load) {
  test(`load ${c.dir}`, () => {
    if (c.error) {
      assert.throws(() => loadMigrations(join(root, c.dir)), (err: Error) => err.message.includes(c.error!));
      return;
    }
    const files = loadMigrations(join(root, c.dir));
    assert.deepEqual(files.map((f) => ({ name: f.name, checksum: f.checksum })), c.files);
  });
}

/** A new, empty database, dropped by the returned cleanup. */
async function freshDatabase(): Promise<{ url: string; drop: () => Promise<void> }> {
  const admin = new pg.Client({ connectionString: url });
  await admin.connect();
  const name = "migrate_ts_" + randomBytes(4).toString("hex");
  await admin.query(`CREATE DATABASE ${name}`);
  const u = new URL(url!);
  u.pathname = "/" + name;
  return {
    url: u.toString(),
    drop: async () => {
      await admin.query(`DROP DATABASE ${name} WITH (FORCE)`);
      await admin.end();
    },
  };
}

test("the shared apply cases", { skip }, async () => {
  const db = await freshDatabase();
  const client = new pg.Client({ connectionString: db.url });
  await client.connect();
  try {
    for (const [i, step] of cases.apply.entries()) {
      const files = loadMigrations(join(root, step.dir));
      if (step.statusBefore) {
        const st = await migrationStatus(client, files);
        assert.deepEqual(st.map((e) => ({ name: e.name, state: e.state })), step.statusBefore, `step ${i} (${step.dir})`);
      }
      let names: string[] = [];
      let error: Error | undefined;
      try {
        names = await applyMigrations(client, files);
      } catch (err) {
        error = err as Error;
      }
      if (step.error) assert.ok(error?.message.includes(step.error), `step ${i} (${step.dir}): ${error}`);
      else assert.equal(error, undefined, `step ${i} (${step.dir})`);
      if (step.applied && !step.error) assert.deepEqual(names, step.applied, `step ${i} (${step.dir})`);
      for (const table of step.tablesAbsent ?? []) {
        const { rows } = await client.query("SELECT to_regclass($1) IS NOT NULL AS exists", [table]);
        assert.equal(rows[0].exists, false, `step ${i}: ${table} exists after the migration failed`);
      }
    }
  } finally {
    await client.end();
    await db.drop();
  }
});

test("concurrent runs apply each migration once", { skip }, async () => {
  const db = await freshDatabase();
  const files = loadMigrations(join(root, "v2"));
  const clients = await Promise.all(
    [0, 1, 2, 3].map(async () => {
      const c = new pg.Client({ connectionString: db.url });
      await c.connect();
      return c;
    }),
  );
  try {
    const results = await Promise.all(clients.map((c) => applyMigrations(c, files)));
    assert.deepEqual(results.flat().sort(), files.map((f) => f.name));
  } finally {
    await Promise.all(clients.map((c) => c.end()));
    await db.drop();
  }
});

test("the runtime applies migrations before it runs a function, and describe lists them", { skip }, async () => {
  const db = await freshDatabase();
  const dir = mkdtempSync(join(tmpdir(), "migrate-app-"));
  cpSync(join(root, "v2"), join(dir, "migrations"), { recursive: true });
  const cli = fileURLToPath(new URL("./cli.ts", import.meta.url));
  const app = fileURLToPath(new URL("./testdata/app.ts", import.meta.url));
  const run = (args: string[], env: Record<string, string>) =>
    new Promise<{ code: number | null; out: string }>((resolve) => {
      const child = spawn(process.execPath, [cli, "--source", app, ...args], { cwd: dir, env: { ...process.env, ...env } });
      let out = "";
      child.stdout.on("data", (d) => (out += d));
      child.stderr.on("data", (d) => (out += d));
      child.on("exit", (code) => resolve({ code, out }));
    });
  try {
    const described = await run(["--describe"], {});
    assert.equal(described.code, 0, described.out);
    assert.deepEqual(JSON.parse(described.out).migrations, ["0001_init.sql", "0002_items.sql", "0003_more.sql"]);

    const first = await run(["--target", "nightly-report"], { DATABASE_URL: db.url });
    assert.equal(first.code, 0, first.out);
    assert.match(first.out, /migrations: applied 3/);
    const again = await run(["--target", "nightly-report"], { DATABASE_URL: db.url });
    assert.equal(again.code, 0, again.out);
    assert.doesNotMatch(again.out, /applied/);

    const noDb = await run(["--target", "nightly-report"], { DATABASE_URL: "" });
    assert.equal(noDb.code, 1, noDb.out);
    assert.match(noDb.out, /DATABASE_URL isn't set/);
    const off = await run(["--target", "nightly-report"], { DATABASE_URL: "", MAXCLOUD_MIGRATE: "0" });
    assert.equal(off.code, 0, off.out);
  } finally {
    rmSync(dir, { recursive: true, force: true });
    await db.drop();
  }
});
