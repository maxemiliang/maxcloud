import { test } from "node:test";
import assert from "node:assert/strict";
import pg from "pg";
import { db } from "./db.ts";
import { defaultPool } from "./queue.ts";

test("db() without DATABASE_URL says where it comes from", () => {
  const saved = process.env.DATABASE_URL;
  delete process.env.DATABASE_URL;
  try {
    assert.throws(() => db(), /DATABASE_URL isn't set.*db: true.*maxcloud\.yaml.*mcloud dev/);
  } finally {
    if (saved !== undefined) process.env.DATABASE_URL = saved;
  }
});

// Needs a Postgres for the query: TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres
const url = process.env.TEST_DATABASE_URL;

test("db() is one shared pg.Pool, the one enqueue and kv use", async () => {
  // A pool doesn't connect until it's used, so without a test database any URL will do.
  process.env.DATABASE_URL = url ?? "postgres://nobody@127.0.0.1:1/none";
  const pool = db();
  try {
    assert.ok(pool instanceof pg.Pool);
    assert.equal(db(), pool);
    assert.equal(defaultPool(), pool);
    if (url) assert.equal((await pool.query("SELECT 1 + 1 AS two")).rows[0].two, 2);
  } finally {
    await pool.end();
  }
});
