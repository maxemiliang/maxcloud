// The job queue: one Postgres table, maxcloud_jobs, in the app's database
// (DATABASE_URL). Workers claim rows with FOR UPDATE SKIP LOCKED, so two never
// take the same job, and hold them on a lease: a worker that dies leaves its
// job to be claimed again once the lease runs out.

import pg from "pg";
import { checkQueueName, type Job, type QueueFunction } from "./define.ts";
import { databaseUrl, db as sharedPool } from "./db.ts";

export const TABLE = "maxcloud_jobs";
const CHANNEL = "maxcloud_jobs";

const SCHEMA = `
CREATE TABLE IF NOT EXISTS ${TABLE} (
  id bigserial PRIMARY KEY,
  queue text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'done', 'failed')),
  attempts integer NOT NULL DEFAULT 0,
  run_after timestamptz NOT NULL DEFAULT now(),
  locked_until timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  done_at timestamptz
);
CREATE INDEX IF NOT EXISTS ${TABLE}_open ON ${TABLE} (queue, run_after, id) WHERE state IN ('pending', 'running');
`;

/**
 * The query KEDA scales a queue's worker on: jobs due or being worked on.
 * Counting running jobs too keeps a worker alive until its jobs finish.
 */
export function backlogQuery(queue: string): string {
  checkQueueName(queue);
  return `SELECT count(*) FROM ${TABLE} WHERE queue = '${queue}' AND state IN ('pending', 'running') AND run_after <= now()`;
}

/** Anything that can run a query: a Pool, or a client inside a transaction. */
export interface Queryable {
  query: pg.Pool["query"];
}

/** The pool `enqueue` uses unless given a client: the app's shared `db()`. */
export const defaultPool = sharedPool;

const ready = new WeakMap<object, Promise<void>>();

/** Creates the table once per pool. Safe to race: the DDL runs under a lock. */
export function ensureSchema(db: pg.Pool): Promise<void> {
  let p = ready.get(db);
  if (!p) {
    p = (async () => {
      const client = await db.connect();
      try {
        await client.query("BEGIN");
        await client.query(`SELECT pg_advisory_xact_lock(hashtext('${TABLE}'))`);
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

export interface EnqueueOptions {
  /** Run no sooner than this many seconds from now. */
  delaySeconds?: number;
  /**
   * Insert with this client, e.g. inside your own transaction, so the job
   * exists only if the transaction commits. The table must already exist
   * (any worker or earlier enqueue creates it).
   */
  client?: Queryable;
}

/**
 * Adds a job to a queue and returns its id.
 *
 * ```ts
 * await enqueue("images", { key: "uploads/cat.jpg" });
 * ```
 */
export async function enqueue<T>(queue: string | QueueFunction<T>, payload: T, options: EnqueueOptions = {}): Promise<string> {
  const name = typeof queue === "string" ? queue : queue.options.queue;
  checkQueueName(name);
  let db: Queryable | undefined = options.client;
  if (!db) {
    const p = defaultPool();
    await ensureSchema(p);
    db = p;
  }
  const delay = Math.max(0, options.delaySeconds ?? 0);
  const res = await db.query<{ id: string }>(
    `INSERT INTO ${TABLE} (queue, payload, run_after) VALUES ($1, $2, now() + make_interval(secs => $3)) RETURNING id`,
    [name, JSON.stringify(payload ?? null), delay],
  );
  await db.query(`SELECT pg_notify('${CHANNEL}', $1)`, [name]);
  return res.rows[0]!.id;
}

interface Row {
  id: string;
  queue: string;
  payload: unknown;
  attempts: number;
  created_at: Date;
}

/** Claims the next due job, or one whose lease ran out. */
export async function claim(db: Queryable, queue: string, leaseSeconds: number): Promise<Job<unknown> | undefined> {
  const res = await db.query<Row>(
    `UPDATE ${TABLE} j
        SET state = 'running', attempts = j.attempts + 1, locked_until = now() + make_interval(secs => $2)
       FROM (SELECT id FROM ${TABLE}
              WHERE queue = $1 AND run_after <= now()
                AND (state = 'pending' OR (state = 'running' AND locked_until < now()))
              ORDER BY run_after, id
              FOR UPDATE SKIP LOCKED
              LIMIT 1) next
      WHERE j.id = next.id
      RETURNING j.id, j.queue, j.payload, j.attempts, j.created_at`,
    [queue, leaseSeconds],
  );
  const r = res.rows[0];
  if (!r) return undefined;
  return { id: r.id, queue: r.queue, payload: r.payload, attempt: r.attempts, createdAt: r.created_at };
}

export async function complete(db: Queryable, id: string): Promise<void> {
  await db.query(`UPDATE ${TABLE} SET state = 'done', done_at = now(), locked_until = NULL WHERE id = $1`, [id]);
}

/** Schedules a retry with exponential backoff, or marks the job failed after its last attempt. */
export async function fail(db: Queryable, job: Job<unknown>, error: unknown, maxAttempts: number, backoffSeconds: number): Promise<"retry" | "failed"> {
  const message = error instanceof Error ? (error.stack ?? error.message) : String(error);
  if (job.attempt >= maxAttempts) {
    await db.query(`UPDATE ${TABLE} SET state = 'failed', done_at = now(), locked_until = NULL, last_error = $2 WHERE id = $1`, [job.id, message]);
    return "failed";
  }
  const delay = backoffSeconds * 2 ** (job.attempt - 1);
  await db.query(
    `UPDATE ${TABLE} SET state = 'pending', locked_until = NULL, last_error = $2, run_after = now() + make_interval(secs => $3) WHERE id = $1`,
    [job.id, message, delay],
  );
  return "retry";
}

/** Puts a job back without counting the attempt, for a worker shutting down. */
export async function release(db: Queryable, id: string): Promise<void> {
  await db.query(`UPDATE ${TABLE} SET state = 'pending', attempts = greatest(attempts - 1, 0), locked_until = NULL WHERE id = $1`, [id]);
}

/** Deletes finished jobs older than the given number of days. */
export async function prune(db: Queryable, days = 7): Promise<number> {
  const res = await db.query(`DELETE FROM ${TABLE} WHERE state = 'done' AND done_at < now() - make_interval(days => $1)`, [days]);
  return res.rowCount ?? 0;
}

export interface WorkerOptions {
  /** Defaults to a pool on DATABASE_URL. */
  pool?: pg.Pool;
  /** Stops the worker: no new jobs; jobs in hand finish, or are put back at `graceMs`. */
  signal: AbortSignal;
  /** How long jobs in hand get to finish after `signal`. Default 25 s. */
  graceMs?: number;
  /** How often an idle worker looks for jobs (it's also woken on enqueue). Default 2 s. */
  pollMs?: number;
  log?: (msg: string) => void;
}

/**
 * Works on a queue until `signal` aborts. Resolves once every job in hand is
 * done or put back.
 */
export async function runWorker(fn: QueueFunction<any>, opts: WorkerOptions): Promise<void> {
  const o = fn.options;
  const concurrency = Math.max(1, o.concurrency ?? 1);
  const timeoutMs = (o.timeoutSeconds ?? 300) * 1000;
  const lease = Math.ceil(timeoutMs / 1000) + 30;
  const maxAttempts = o.maxAttempts ?? 5;
  const backoff = o.backoffSeconds ?? 10;
  const log = opts.log ?? ((m: string) => console.log(m));
  const db = opts.pool ?? new pg.Pool({ connectionString: databaseUrl(), max: concurrency + 1 });
  const ownPool = !opts.pool;

  await ensureSchema(db);
  await prune(db).catch(() => {});

  // Wake idle slots when a job is enqueued, instead of waiting for the poll.
  let wake: () => void = () => {};
  let wakeup = new Promise<void>((r) => (wake = r));
  const poke = () => {
    wake();
    wakeup = new Promise<void>((r) => (wake = r));
  };
  const listener = await db.connect();
  listener.on("notification", (n) => {
    if (n.payload === o.queue) poke();
  });
  await listener.query(`LISTEN ${CHANNEL}`);
  opts.signal.addEventListener("abort", poke, { once: true });

  const stopping = opts.signal;
  const shutdown = new AbortController();
  stopping.addEventListener("abort", () => setTimeout(() => shutdown.abort(), opts.graceMs ?? 25_000).unref(), { once: true });

  const work = async (job: Job<unknown>) => {
    const timeout = AbortSignal.timeout(timeoutMs);
    const signal = AbortSignal.any([timeout, shutdown.signal]);
    try {
      await Promise.race([
        fn.handler(job, { signal }),
        new Promise((_, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true })),
      ]);
      await complete(db, job.id);
      log(`job ${job.id} done (attempt ${job.attempt})`);
    } catch (err) {
      if (shutdown.signal.aborted && !timeout.aborted) {
        await release(db, job.id);
        log(`job ${job.id} put back: worker shutting down`);
        return;
      }
      const reason = timeout.aborted ? new Error(`timed out after ${timeoutMs / 1000}s`) : err;
      const outcome = await fail(db, job, reason, maxAttempts, backoff);
      log(`job ${job.id} ${outcome === "failed" ? "failed for good" : "failed, will retry"} (attempt ${job.attempt}/${maxAttempts}): ${reason instanceof Error ? reason.message : String(reason)}`);
    }
  };

  const slot = async () => {
    while (!stopping.aborted) {
      let job: Job<unknown> | undefined;
      try {
        job = await claim(db, o.queue, lease);
      } catch (err) {
        log(`claiming from ${o.queue}: ${err instanceof Error ? err.message : String(err)}`);
      }
      if (job) {
        await work(job);
        continue;
      }
      await Promise.race([wakeup, new Promise((r) => setTimeout(r, opts.pollMs ?? 2000).unref())]);
    }
  };

  log(`worker for queue ${o.queue} started (concurrency ${concurrency})`);
  try {
    await Promise.all(Array.from({ length: concurrency }, slot));
  } finally {
    await listener.query(`UNLISTEN ${CHANNEL}`).catch(() => {});
    listener.release();
    if (ownPool) await db.end();
  }
  log(`worker for queue ${o.queue} stopped`);
}
