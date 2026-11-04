package functions

// The job queue: one Postgres table, maxcloud_jobs, in the app's database
// (DATABASE_URL), shared with the Node.js SDK. Workers claim rows with
// FOR UPDATE SKIP LOCKED, so two never take the same job, and hold them on a
// lease: a worker that dies leaves its job to be claimed again once the
// lease runs out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	table          = "maxcloud_jobs"
	channel        = "maxcloud_jobs"
	defaultBackoff = 10 * time.Second
)

const schema = `
CREATE TABLE IF NOT EXISTS ` + table + ` (
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
CREATE INDEX IF NOT EXISTS ` + table + `_open ON ` + table + ` (queue, run_after, id) WHERE state IN ('pending', 'running');
`

// BacklogQuery is the query KEDA scales a queue's worker on: jobs due or
// being worked on. Counting running jobs too keeps a worker alive until its
// jobs finish.
func BacklogQuery(queue string) string {
	checkQueue(queue)
	return fmt.Sprintf("SELECT count(*) FROM %s WHERE queue = '%s' AND state IN ('pending', 'running') AND run_after <= now()", table, queue)
}

// A Job is one entry in a queue.
type Job struct {
	ID        int64
	Queue     string
	Payload   json.RawMessage
	Attempt   int // 1 on the first try
	CreatedAt time.Time
}

// Decode unmarshals the job's payload into v.
func (j *Job) Decode(v any) error { return json.Unmarshal(j.Payload, v) }

// DB runs queries: a *pgxpool.Pool, a *pgx.Conn or a pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	poolErr  error
)

// Pool is the connection pool on $DATABASE_URL that Enqueue uses by default.
func Pool(ctx context.Context) (*pgxpool.Pool, error) {
	poolOnce.Do(func() {
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			poolErr = errors.New("functions: DATABASE_URL isn't set: queues need the app's Postgres database")
			return
		}
		pool, poolErr = pgxpool.New(ctx, url)
	})
	return pool, poolErr
}

var schemaReady sync.Map // *pgxpool.Pool -> struct{}

// ensureSchema creates the table once per pool. Safe to race: the DDL runs
// under a lock.
func ensureSchema(ctx context.Context, p *pgxpool.Pool) error {
	if _, ok := schemaReady.Load(p); ok {
		return nil
	}
	err := pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('"+table+"'))"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
	if err == nil {
		schemaReady.Store(p, struct{}{})
	}
	return err
}

type enqueueOptions struct {
	delay time.Duration
	db    DB
}

// An EnqueueOption changes how Enqueue adds a job.
type EnqueueOption func(*enqueueOptions)

// Delay makes the job wait at least d before it runs.
func Delay(d time.Duration) EnqueueOption { return func(o *enqueueOptions) { o.delay = d } }

// In inserts the job with db, e.g. your own transaction, so the job exists
// only if it commits. The table must already exist (any worker, or an
// earlier Enqueue without In, creates it).
func In(db DB) EnqueueOption { return func(o *enqueueOptions) { o.db = db } }

// Enqueue adds a job to a queue and returns its id. The payload is stored as
// JSON.
func Enqueue(ctx context.Context, queue string, payload any, opts ...EnqueueOption) (int64, error) {
	checkQueue(queue)
	var o enqueueOptions
	for _, opt := range opts {
		opt(&o)
	}
	db := o.db
	if db == nil {
		p, err := Pool(ctx)
		if err != nil {
			return 0, err
		}
		if err := ensureSchema(ctx, p); err != nil {
			return 0, err
		}
		db = p
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("functions: job payload: %w", err)
	}
	var id int64
	err = db.QueryRow(ctx,
		`INSERT INTO `+table+` (queue, payload, run_after) VALUES ($1, $2, now() + make_interval(secs => $3)) RETURNING id`,
		queue, data, max(o.delay.Seconds(), 0)).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = db.Exec(ctx, "SELECT pg_notify('"+channel+"', $1)", queue)
	return id, err
}

func claim(ctx context.Context, db DB, queue string, lease time.Duration) (*Job, error) {
	var j Job
	err := db.QueryRow(ctx, `UPDATE `+table+` j
	    SET state = 'running', attempts = j.attempts + 1, locked_until = now() + make_interval(secs => $2)
	   FROM (SELECT id FROM `+table+`
	          WHERE queue = $1 AND run_after <= now()
	            AND (state = 'pending' OR (state = 'running' AND locked_until < now()))
	          ORDER BY run_after, id
	          FOR UPDATE SKIP LOCKED
	          LIMIT 1) next
	  WHERE j.id = next.id
	  RETURNING j.id, j.queue, j.payload, j.attempts, j.created_at`,
		queue, lease.Seconds()).Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempt, &j.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func complete(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `UPDATE `+table+` SET state = 'done', done_at = now(), locked_until = NULL WHERE id = $1`, id)
	return err
}

// fail schedules a retry with exponential backoff, or marks the job failed
// after its last attempt. Reports whether it will be retried.
func fail(ctx context.Context, db DB, j *Job, cause error, maxAttempts int, backoff time.Duration) (bool, error) {
	if j.Attempt >= maxAttempts {
		_, err := db.Exec(ctx, `UPDATE `+table+` SET state = 'failed', done_at = now(), locked_until = NULL, last_error = $2 WHERE id = $1`, j.ID, cause.Error())
		return false, err
	}
	delay := backoff.Seconds() * math.Pow(2, float64(j.Attempt-1))
	_, err := db.Exec(ctx, `UPDATE `+table+` SET state = 'pending', locked_until = NULL, last_error = $2, run_after = now() + make_interval(secs => $3) WHERE id = $1`, j.ID, cause.Error(), delay)
	return true, err
}

// release puts a job back without counting the attempt, for a worker
// shutting down.
func release(ctx context.Context, db DB, id int64) error {
	_, err := db.Exec(ctx, `UPDATE `+table+` SET state = 'pending', attempts = greatest(attempts - 1, 0), locked_until = NULL WHERE id = $1`, id)
	return err
}

func prune(ctx context.Context, db DB, days int) error {
	_, err := db.Exec(ctx, `DELETE FROM `+table+` WHERE state = 'done' AND done_at < now() - make_interval(days => $1)`, days)
	return err
}

type workerConfig struct {
	pool  *pgxpool.Pool
	grace time.Duration // after stop, how long jobs in hand get to finish
	poll  time.Duration
	logf  func(format string, args ...any)
}

var errShutdown = errors.New("worker shutting down")

// runWorker works on f's queue until stop is done, then lets the jobs in
// hand finish (or puts them back after the grace period) and returns.
func runWorker(stop context.Context, f *function, cfg workerConfig) error {
	o := f.opts
	concurrency := max(o.concurrency, 1)
	timeout := time.Duration(f.jobTimeout()) * time.Second
	lease := timeout + 30*time.Second
	maxAttempts := or(o.maxAttempts, 5)
	backoff := or(o.backoff, defaultBackoff)
	if cfg.grace == 0 {
		cfg.grace = 25 * time.Second
	}
	if cfg.poll == 0 {
		cfg.poll = 2 * time.Second
	}
	if cfg.logf == nil {
		cfg.logf = log.Printf
	}
	db := cfg.pool
	// Database writes still have to happen while stopping.
	bg := context.WithoutCancel(stop)

	if err := ensureSchema(bg, db); err != nil {
		return err
	}
	_ = prune(bg, db, 7)

	// Jobs in hand are cancelled once the grace period after stop is over.
	hard, cancelHard := context.WithCancelCause(bg)
	defer cancelHard(nil)
	go func() {
		<-stop.Done()
		select {
		case <-time.After(cfg.grace):
			cancelHard(errShutdown)
		case <-hard.Done():
		}
	}()

	// Wake idle slots when a job is enqueued, instead of waiting for the poll.
	wake := make(chan struct{}, concurrency)
	listenCtx, stopListen := context.WithCancel(stop)
	defer stopListen()
	go func() {
		conn, err := db.Acquire(listenCtx)
		if err != nil {
			return
		}
		defer conn.Release()
		if _, err := conn.Exec(listenCtx, "LISTEN "+channel); err != nil {
			return
		}
		for {
			n, err := conn.Conn().WaitForNotification(listenCtx)
			if err != nil {
				// The connection may be mid-wait; don't hand it back to the pool.
				conn.Hijack().Close(bg)
				return
			}
			if n.Payload == f.queue {
				for range concurrency {
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
		}
	}()

	work := func(j *Job) {
		jobCtx, cancel := context.WithTimeoutCause(hard, timeout, fmt.Errorf("timed out after %s", timeout))
		defer cancel()
		done := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					done <- fmt.Errorf("panic: %v", r)
				}
			}()
			done <- f.job(jobCtx, j)
		}()
		var err error
		select {
		case err = <-done:
		case <-jobCtx.Done():
			err = context.Cause(jobCtx)
		}
		switch {
		case err == nil:
			if err := complete(bg, db, j.ID); err != nil {
				cfg.logf("job %d: marking done: %v", j.ID, err)
				return
			}
			cfg.logf("job %d done (attempt %d)", j.ID, j.Attempt)
		case errors.Is(context.Cause(hard), errShutdown) && jobCtx.Err() != nil:
			_ = release(bg, db, j.ID)
			cfg.logf("job %d put back: worker shutting down", j.ID)
		default:
			retry, ferr := fail(bg, db, j, err, maxAttempts, backoff)
			if ferr != nil {
				cfg.logf("job %d: recording the failure: %v", j.ID, ferr)
				return
			}
			outcome := "failed for good"
			if retry {
				outcome = "failed, will retry"
			}
			cfg.logf("job %d %s (attempt %d/%d): %v", j.ID, outcome, j.Attempt, maxAttempts, err)
		}
	}

	var wg sync.WaitGroup
	for range concurrency {
		wg.Go(func() {
			for stop.Err() == nil {
				j, err := claim(bg, db, f.queue, lease)
				if err != nil {
					cfg.logf("claiming from %s: %v", f.queue, err)
				}
				if j != nil {
					work(j)
					continue
				}
				select {
				case <-stop.Done():
				case <-wake:
				case <-time.After(cfg.poll):
				}
			}
		})
	}
	cfg.logf("worker for queue %s started (concurrency %d)", f.queue, concurrency)
	wg.Wait()
	cfg.logf("worker for queue %s stopped", f.queue)
	return nil
}
