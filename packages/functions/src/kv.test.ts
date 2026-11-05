// Needs a Postgres: TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { kv } from "./kv.ts";

const url = process.env.TEST_DATABASE_URL;
const skip = url ? false : "set TEST_DATABASE_URL to run the kv tests";

before(() => {
  if (url) process.env.DATABASE_URL = url;
});
after(async () => {
  const { defaultPool } = await import("./queue.ts");
  if (url) await defaultPool().end();
});

const prefix = () => "t" + randomUUID().slice(0, 8) + ":";

test("set, get, overwrite and delete", { skip }, async () => {
  const p = prefix();
  assert.equal(await kv.get(p + "a"), undefined);
  await kv.set(p + "a", { n: 1, tags: ["x"] });
  assert.deepEqual(await kv.get(p + "a"), { n: 1, tags: ["x"] });
  await kv.set(p + "a", "now a string");
  assert.equal(await kv.get(p + "a"), "now a string");
  assert.equal(await kv.delete(p + "a"), true);
  assert.equal(await kv.delete(p + "a"), false);
  assert.equal(await kv.get(p + "a"), undefined);
});

test("expired keys read as missing", { skip }, async () => {
  const p = prefix();
  await kv.set(p + "short", 1, { ttlSeconds: 0.2 });
  await kv.set(p + "long", 2, { ttlSeconds: 3600 });
  assert.equal(await kv.get(p + "short"), 1);
  await new Promise((r) => setTimeout(r, 400));
  assert.equal(await kv.get(p + "short"), undefined);
  assert.equal(await kv.get(p + "long"), 2);
  assert.deepEqual((await kv.list({ prefix: p })).keys, [p + "long"]);
});

test("list pages through a prefix in order", { skip }, async () => {
  const p = prefix();
  for (const k of ["c", "a", "b", "d", "e"]) await kv.set(p + k, k);
  await kv.set(prefix() + "other", 0);
  const first = await kv.list({ prefix: p, limit: 2 });
  assert.deepEqual(first.keys, [p + "a", p + "b"]);
  const second = await kv.list({ prefix: p, limit: 2, cursor: first.cursor });
  assert.deepEqual(second.keys, [p + "c", p + "d"]);
  const last = await kv.list({ prefix: p, limit: 2, cursor: second.cursor });
  assert.deepEqual(last.keys, [p + "e"]);
  assert.equal(last.cursor, undefined);
});

test("bad input is refused", { skip }, async () => {
  await assert.rejects(kv.get(""), /1 to 512/);
  await assert.rejects(kv.set("k", undefined), /undefined/);
  await assert.rejects(kv.set("k", 1, { ttlSeconds: 0 }), /positive/);
});
