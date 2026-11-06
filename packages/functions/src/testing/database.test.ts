// Needs a Postgres: TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres
import { test } from "node:test";
import assert from "node:assert/strict";
import pg from "pg";
import { db } from "../db.ts";
import { kv } from "../kv.ts";
import { enqueue, TABLE } from "../queue.ts";
import { migrate } from "../migrate.ts";
import { fileURLToPath } from "node:url";
import { testDatabase, withSearchPath } from "./database.ts";

const url = process.env.TEST_DATABASE_URL;
const skip = url ? false : "set TEST_DATABASE_URL to run the testDatabase tests";

test("withSearchPath adds to the URL's startup options", () => {
  const u = new URL(withSearchPath("postgres://u:p@h:5432/db?sslmode=disable", "s1"));
  assert.equal(u.searchParams.get("options"), "-c search_path=s1");
  assert.equal(u.searchParams.get("sslmode"), "disable");
  const v = new URL(withSearchPath("postgres://h/db?options=-c%20statement_timeout%3D5s", "s2"));
  assert.equal(v.searchParams.get("options"), "-c statement_timeout=5s -c search_path=s2");
  assert.throws(() => withSearchPath("host=h dbname=x", "s"), /isn't a postgres:\/\/ URL/);
});

test("testDatabase without TEST_DATABASE_URL says what to set", async () => {
  const saved = process.env.TEST_DATABASE_URL;
  delete process.env.TEST_DATABASE_URL;
  try {
    await assert.rejects(testDatabase(), /TEST_DATABASE_URL isn't set/);
  } finally {
    if (saved !== undefined) process.env.TEST_DATABASE_URL = saved;
  }
});

test("db(), kv and enqueue use the schema, and drop() removes it", { skip }, async () => {
  const before = process.env.DATABASE_URL;
  const t = await testDatabase();
  try {
    assert.equal(db(), t.pool);
    assert.equal(process.env.DATABASE_URL, t.url);
    assert.equal((await db().query("SELECT current_schema() AS s")).rows[0].s, t.schema);

    await db().query("CREATE TABLE users (id int PRIMARY KEY)");
    await kv.set("k", { n: 1 });
    assert.deepEqual(await kv.get("k"), { n: 1 });
    await enqueue("mail", { to: "ann" });

    // Everything landed in the schema.
    const tables = await t.pool.query(
      "SELECT table_name FROM information_schema.tables WHERE table_schema = $1 ORDER BY table_name",
      [t.schema],
    );
    assert.deepEqual(
      tables.rows.map((r) => r.table_name),
      [TABLE, "maxcloud_kv", "users"],
    );
    // A plain connection on t.url (a worker's, a child process's) is in the schema too.
    const c = new pg.Client({ connectionString: t.url });
    await c.connect();
    assert.equal((await c.query("SELECT count(*)::int AS n FROM maxcloud_jobs")).rows[0].n, 1);
    await c.end();
  } finally {
    await t.drop();
  }
  await t.drop(); // twice is fine
  assert.equal(process.env.DATABASE_URL, before);

  const admin = new pg.Client({ connectionString: url });
  await admin.connect();
  const left = await admin.query("SELECT 1 FROM pg_namespace WHERE nspname = $1", [t.schema]);
  await admin.end();
  assert.equal(left.rowCount, 0);
});

test("two test databases don't see each other's rows", { skip }, async () => {
  const a = await testDatabase({ use: false });
  const b = await testDatabase({ use: false });
  try {
    assert.notEqual(a.schema, b.schema);
    await a.pool.query("CREATE TABLE t (v text)");
    await a.pool.query("INSERT INTO t VALUES ('a')");
    await assert.rejects(b.pool.query("SELECT * FROM t"), /relation "t" does not exist/);
  } finally {
    await a.drop();
    await b.drop();
  }
});

test("drop() puts back the pool db() had", { skip }, async () => {
  const outer = await testDatabase();
  const inner = await testDatabase();
  assert.equal(db(), inner.pool);
  await inner.drop();
  assert.equal(db(), outer.pool);
  assert.equal(process.env.DATABASE_URL, outer.url);
  await outer.drop();
});

test("migrate() applies the migrations in every test database, not just the first", { skip }, async () => {
  const dir = fileURLToPath(new URL("../../../../testdata/migrate/v1/", import.meta.url));
  for (let i = 0; i < 2; i++) {
    const t = await testDatabase();
    try {
      assert.deepEqual(await migrate({ dir }), ["0001_init.sql", "0002_items.sql"], `test database ${i + 1}`);
      const tables = await t.pool.query("SELECT table_name FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'maxcloud_migrations'", [t.schema]);
      assert.equal(tables.rowCount, 1, "the table that records them is in the schema");
    } finally {
      await t.drop();
    }
  }
});
