// Needs a Postgres: TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres
import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import pg from "pg";
import { onQueue } from "./define.ts";
import { backlogQuery, claim, enqueue, ensureSchema, fail, runWorker, TABLE } from "./queue.ts";

const url = process.env.TEST_DATABASE_URL;
const skip = url ? false : "set TEST_DATABASE_URL to run the queue tests";
let db: pg.Pool;

before(async () => {
  if (!url) return;
  process.env.DATABASE_URL = url;
  db = new pg.Pool({ connectionString: url, max: 10 });
  await ensureSchema(db);
});
after(async () => {
  await db?.end();
  const { defaultPool } = await import("./queue.ts");
  if (url) await defaultPool().end();
});

const q = () => "t" + randomUUID().slice(0, 8);
const backlog = async (queue: string) => Number((await db.query(backlogQuery(queue))).rows[0].count);
const state = async (id: string) => (await db.query(`SELECT state, attempts, last_error, run_after > now() AS later FROM ${TABLE} WHERE id = $1`, [id])).rows[0];

test("enqueue, claim and the backlog count", { skip }, async () => {
  const queue = q();
  const id = await enqueue(queue, { n: 1 });
  await enqueue(queue, { n: 2 }, { delaySeconds: 3600 });
  assert.equal(await backlog(queue), 1, "a delayed job isn't due yet");
  const job = await claim(db, queue, 60);
  assert.equal(job?.id, id);
  assert.deepEqual(job?.payload, { n: 1 });
  assert.equal(job?.attempt, 1);
  assert.equal(await backlog(queue), 1, "a running job still counts, so KEDA keeps its worker");
  assert.equal(await claim(db, queue, 60), undefined);
});

test("parallel claims never take the same job", { skip }, async () => {
  const queue = q();
  for (let i = 0; i < 20; i++) await enqueue(queue, i);
  const jobs = await Promise.all(Array.from({ length: 20 }, () => claim(db, queue, 60)));
  const ids = new Set(jobs.map((j) => j?.id));
  assert.equal(ids.size, 20);
  assert.ok(!ids.has(undefined));
});

test("failures back off, then fail for good", { skip }, async () => {
  const queue = q();
  const id = await enqueue(queue, {});
  const job = (await claim(db, queue, 60))!;
  assert.equal(await fail(db, job, new Error("nope"), 2, 60), "retry");
  let s = await state(id);
  assert.equal(s.state, "pending");
  assert.equal(s.later, true);
  assert.match(s.last_error, /nope/);
  await db.query(`UPDATE ${TABLE} SET run_after = now() WHERE id = $1`, [id]);
  const again = (await claim(db, queue, 60))!;
  assert.equal(again.attempt, 2);
  assert.equal(await fail(db, again, new Error("still no"), 2, 60), "failed");
  s = await state(id);
  assert.equal(s.state, "failed");
  assert.equal(await backlog(queue), 0);
});

test("a job whose lease ran out is claimed again", { skip }, async () => {
  const queue = q();
  const id = await enqueue(queue, {});
  await claim(db, queue, 60);
  assert.equal(await claim(db, queue, 60), undefined);
  await db.query(`UPDATE ${TABLE} SET locked_until = now() - interval '1 second' WHERE id = $1`, [id]);
  const again = await claim(db, queue, 60);
  assert.equal(again?.id, id);
  assert.equal(again?.attempt, 2);
});

test("a worker runs jobs, retries a failure and stops cleanly", { skip }, async () => {
  const queue = q();
  const seen: string[] = [];
  const fn = onQueue<{ n: number }>({ queue, concurrency: 2, backoffSeconds: 0, maxAttempts: 3 }, async (job) => {
    if (job.payload.n === 3 && job.attempt === 1) throw new Error("flaky");
    seen.push(`${job.payload.n}#${job.attempt}`);
  });
  const stop = new AbortController();
  const running = runWorker(fn, { pool: db, signal: stop.signal, pollMs: 50, log: () => {} });
  for (const n of [1, 2, 3]) await enqueue(queue, { n });
  const deadline = Date.now() + 5000;
  while (seen.length < 3 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 25));
  stop.abort();
  await running;
  assert.deepEqual(seen.sort(), ["1#1", "2#1", "3#2"]);
  assert.equal(await backlog(queue), 0);
});

test("a worker stopping puts back the job it can't finish", { skip }, async () => {
  const queue = q();
  let started!: () => void;
  const isRunning = new Promise<void>((r) => (started = r));
  const fn = onQueue({ queue }, async (_job, { signal }) => {
    started();
    await new Promise((_, reject) => signal.addEventListener("abort", () => reject(signal.reason)));
  });
  const stop = new AbortController();
  const running = runWorker(fn, { pool: db, signal: stop.signal, pollMs: 50, graceMs: 100, log: () => {} });
  const id = await enqueue(queue, {});
  await isRunning;
  stop.abort();
  await running;
  const s = await state(id);
  assert.equal(s.state, "pending");
  assert.equal(s.attempts, 0, "a shutdown doesn't use up an attempt");
});

test("a job past its timeout fails and is retried", { skip }, async () => {
  const queue = q();
  const fn = onQueue({ queue, timeoutSeconds: 0.2, backoffSeconds: 60 }, () => new Promise(() => {}));
  const stop = new AbortController();
  const running = runWorker(fn, { pool: db, signal: stop.signal, pollMs: 50, log: () => {} });
  const id = await enqueue(queue, {});
  const deadline = Date.now() + 5000;
  let s;
  while (Date.now() < deadline) {
    s = await state(id);
    if (s.last_error) break;
    await new Promise((r) => setTimeout(r, 50));
  }
  stop.abort();
  await running;
  assert.equal(s.state, "pending");
  assert.match(s.last_error, /timed out/);
});
