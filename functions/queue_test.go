package functions

// Needs a Postgres: TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run the queue tests")
	}
	t.Setenv("DATABASE_URL", url)
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := ensureSchema(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func queueName() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "g" + hex.EncodeToString(b)
}

func backlog(t *testing.T, p *pgxpool.Pool, q string) int {
	var n int
	if err := p.QueryRow(context.Background(), BacklogQuery(q)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type row struct {
	State     string
	Attempts  int
	LastError *string
	Later     bool
}

func state(t *testing.T, p *pgxpool.Pool, id int64) row {
	var r row
	err := p.QueryRow(context.Background(), `SELECT state, attempts, last_error, run_after > now() FROM `+table+` WHERE id = $1`, id).Scan(&r.State, &r.Attempts, &r.LastError, &r.Later)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEnqueueClaimBacklog(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	q := queueName()
	id, err := Enqueue(ctx, q, map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(ctx, q, 2, Delay(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := backlog(t, p, q); n != 1 {
		t.Errorf("backlog %d, want 1: a delayed job isn't due", n)
	}
	j, err := claim(ctx, p, q, time.Minute)
	if err != nil || j == nil || j.ID != id || j.Attempt != 1 {
		t.Fatalf("claim %+v %v", j, err)
	}
	var payload map[string]int
	if err := j.Decode(&payload); err != nil || payload["n"] != 1 {
		t.Errorf("payload %s", j.Payload)
	}
	if n := backlog(t, p, q); n != 1 {
		t.Errorf("backlog %d: a running job still counts", n)
	}
	if j, _ := claim(ctx, p, q, time.Minute); j != nil {
		t.Errorf("claimed %d twice", j.ID)
	}
}

func TestEnqueueInTransaction(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	q := queueName()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(ctx, q, "x", In(tx)); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	if n := backlog(t, p, q); n != 0 {
		t.Errorf("a rolled-back job exists (%d)", n)
	}
}

func TestParallelClaims(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	q := queueName()
	for i := range 20 {
		Enqueue(ctx, q, i)
	}
	var mu sync.Mutex
	seen := map[int64]bool{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			j, err := claim(ctx, p, q, time.Minute)
			if err != nil || j == nil {
				t.Errorf("claim: %v %v", j, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[j.ID] {
				t.Errorf("job %d claimed twice", j.ID)
			}
			seen[j.ID] = true
		})
	}
	wg.Wait()
}

func TestFailBackoffThenFailed(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	q := queueName()
	id, _ := Enqueue(ctx, q, nil)
	j, _ := claim(ctx, p, q, time.Minute)
	if retry, err := fail(ctx, p, j, errors.New("nope"), 2, time.Minute); err != nil || !retry {
		t.Fatalf("retry %v %v", retry, err)
	}
	if r := state(t, p, id); r.State != "pending" || !r.Later || *r.LastError != "nope" {
		t.Errorf("%+v", r)
	}
	p.Exec(ctx, `UPDATE `+table+` SET run_after = now() WHERE id = $1`, id)
	j, _ = claim(ctx, p, q, time.Minute)
	if j.Attempt != 2 {
		t.Errorf("attempt %d", j.Attempt)
	}
	if retry, _ := fail(ctx, p, j, errors.New("still no"), 2, time.Minute); retry {
		t.Error("retried past maxAttempts")
	}
	if r := state(t, p, id); r.State != "failed" {
		t.Errorf("%+v", r)
	}
}

func TestLeaseExpiry(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	q := queueName()
	id, _ := Enqueue(ctx, q, nil)
	claim(ctx, p, q, time.Minute)
	p.Exec(ctx, `UPDATE `+table+` SET locked_until = now() - interval '1 second' WHERE id = $1`, id)
	j, _ := claim(ctx, p, q, time.Minute)
	if j == nil || j.ID != id || j.Attempt != 2 {
		t.Errorf("reclaim %+v", j)
	}
}

func quiet(string, ...any) {}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestWorkerRunsRetriesAndStops(t *testing.T) {
	p := testPool(t)
	q := queueName()
	var mu sync.Mutex
	var seen []string
	f := &function{name: "w", kind: kindQueue, queue: q, opts: options{concurrency: 2, backoff: time.Millisecond, maxAttempts: 3},
		job: func(ctx context.Context, j *Job) error {
			var n int
			j.Decode(&n)
			if n == 3 && j.Attempt == 1 {
				return errors.New("flaky")
			}
			if n == 4 && j.Attempt == 1 {
				panic("boom")
			}
			mu.Lock()
			seen = append(seen, string(j.Payload)+"#"+string(rune('0'+j.Attempt)))
			mu.Unlock()
			return nil
		}}
	stop, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWorker(stop, f, workerConfig{pool: p, poll: 50 * time.Millisecond, logf: quiet}) }()
	for _, n := range []int{1, 2, 3, 4} {
		Enqueue(context.Background(), q, n)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == 4 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sort.Strings(seen)
	if got := seen; got[0] != "1#1" || got[1] != "2#1" || got[2] != "3#2" || got[3] != "4#2" {
		t.Errorf("seen %v", got)
	}
	if n := backlog(t, p, q); n != 0 {
		t.Errorf("backlog %d", n)
	}
}

func TestWorkerPutsBackOnShutdown(t *testing.T) {
	p := testPool(t)
	q := queueName()
	started := make(chan struct{})
	f := &function{name: "w", kind: kindQueue, queue: q, job: func(ctx context.Context, j *Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	stop, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runWorker(stop, f, workerConfig{pool: p, poll: 50 * time.Millisecond, grace: 100 * time.Millisecond, logf: quiet})
	}()
	id, _ := Enqueue(context.Background(), q, nil)
	<-started
	cancel()
	<-done
	if r := state(t, p, id); r.State != "pending" || r.Attempts != 0 {
		t.Errorf("%+v: want pending with no attempt used", r)
	}
}

func TestWorkerTimeout(t *testing.T) {
	p := testPool(t)
	q := queueName()
	f := &function{name: "w", kind: kindQueue, queue: q, opts: options{timeout: 200 * time.Millisecond, backoff: time.Minute},
		job: func(ctx context.Context, j *Job) error { select {} }}
	stop, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWorker(stop, f, workerConfig{pool: p, poll: 50 * time.Millisecond, logf: quiet}) }()
	id, _ := Enqueue(context.Background(), q, nil)
	waitFor(t, func() bool { return state(t, p, id).LastError != nil })
	cancel()
	<-done
	if r := state(t, p, id); r.State != "pending" || *r.LastError == "" {
		t.Errorf("%+v", r)
	}
}

// A worker's slots claim and finish jobs on a pool of their own (one
// connection each, and one for LISTEN), not the app's: handlers holding
// all of the app's connections don't stop the other slots from claiming.
func TestWorkerHasItsOwnPool(t *testing.T) {
	p := testPool(t)
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 4 // the app's pool on a pod with up to 4 CPUs
	app, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	q := queueName()
	var wg sync.WaitGroup
	wg.Add(4)
	all := make(chan struct{})
	go func() { wg.Wait(); close(all) }()
	f := &function{name: "w", kind: kindQueue, queue: q, opts: options{concurrency: 4},
		job: func(ctx context.Context, j *Job) error {
			c, err := app.Acquire(ctx)
			if err != nil {
				return err
			}
			defer c.Release()
			wg.Done()
			select {
			case <-all:
			case <-ctx.Done():
			}
			return nil
		}}
	stop, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runWorker(stop, f, workerConfig{url: os.Getenv("TEST_DATABASE_URL"), poll: 50 * time.Millisecond, grace: 100 * time.Millisecond, logf: quiet})
	}()
	for i := range 4 {
		if _, err := Enqueue(context.Background(), q, i, In(p)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-all:
	case <-time.After(5 * time.Second):
		t.Error("the 4 handlers were never in flight together")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWorkerPoolSize(t *testing.T) {
	for _, c := range []struct{ concurrency, want int }{{0, 2}, {1, 2}, {10, 11}} {
		p, err := workerPool(context.Background(), "postgres://u@127.0.0.1:1/db?pool_max_conns=3", c.concurrency)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Config().MaxConns; int(got) != c.want {
			t.Errorf("concurrency %d: %d connections, want %d", c.concurrency, got, c.want)
		}
		p.Close()
	}
	if _, err := workerPool(context.Background(), "", 1); err == nil {
		t.Error("no error without DATABASE_URL")
	}
}
