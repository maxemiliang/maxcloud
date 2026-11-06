// Package functions writes HTTP, scheduled and queue functions for maxcloud,
// the homelab's run platform. Register them in main and call Start:
//
//	func main() {
//		functions.HTTPFunc("hello", hello, functions.Public())
//		functions.Schedule("nightly", "0 3 * * *", nightly)
//		functions.Queue("resize", "images", resize)
//		functions.Start()
//	}
//
// Each function is deployed on its own: an HTTP function as a Knative service
// that scales to zero, a schedule as a CronJob, a queue function as a worker
// that KEDA starts when its queue has jobs. The container runs the same
// binary for all of them; Start runs the one named in $FUNCTION_TARGET.
package functions

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type kind string

const (
	kindHTTP     kind = "http"
	kindSchedule kind = "schedule"
	kindQueue    kind = "queue"
)

type function struct {
	name string
	kind kind
	opts options

	http     http.Handler
	schedule string
	run      func(context.Context) error
	queue    string
	job      func(context.Context, *Job) error
}

type options struct {
	public                bool
	minInstances          int
	maxInstances          int
	concurrency           int
	timeout               time.Duration
	memory, cpu, timeZone string
	retries, maxAttempts  int
	backoff               time.Duration
}

// An Option tunes a function. Options that don't apply to a function's kind
// are ignored.
type Option func(*options)

// Public makes an HTTP function reachable from the internet, at
// https://<name>-<app>.gronblom.dev. Without it, it's cluster-local.
func Public() Option { return func(o *options) { o.public = true } }

// MinInstances keeps n instances of an HTTP function running when idle
// (default 0: scale to zero).
func MinInstances(n int) Option { return func(o *options) { o.minInstances = n } }

// MaxInstances caps an HTTP or queue function's instances (default 3).
func MaxInstances(n int) Option { return func(o *options) { o.maxInstances = n } }

// Concurrency is, for an HTTP function, the requests one instance takes at
// once before another starts (default 0: no limit); for a queue function,
// the jobs one instance works on at once (default 1).
func Concurrency(n int) Option { return func(o *options) { o.concurrency = n } }

// Timeout limits one request (default 5m, at most 10m), one scheduled run
// (default 1h) or one job (default 5m).
func Timeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// Memory the container requests and is limited to, e.g. "256Mi".
func Memory(q string) Option { return func(o *options) { o.memory = q } }

// CPU the container requests, e.g. "100m".
func CPU(q string) Option { return func(o *options) { o.cpu = q } }

// TimeZone for a schedule, an IANA name (default UTC).
func TimeZone(tz string) Option { return func(o *options) { o.timeZone = tz } }

// Retries is how often a failed scheduled run is retried (default 0).
func Retries(n int) Option { return func(o *options) { o.retries = n } }

// MaxAttempts is how often a job is tried before it's marked failed (default 5).
func MaxAttempts(n int) Option { return func(o *options) { o.maxAttempts = n } }

// Backoff is the delay before a job's first retry, doubled for each later
// one (default 10s).
func Backoff(d time.Duration) Option { return func(o *options) { o.backoff = d } }

var (
	mu       sync.Mutex
	registry = map[string]*function{}
	nameRe   = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$`)
	queueRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

func register(f *function, opts []Option) {
	if !nameRe.MatchString(f.name) {
		panic(fmt.Sprintf("functions: name %q: use at most 40 lowercase letters, digits and dashes, starting with a letter", f.name))
	}
	for _, o := range opts {
		o(&f.opts)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[f.name]; dup {
		panic(fmt.Sprintf("functions: %s registered twice", f.name))
	}
	registry[f.name] = f
}

// HTTP registers an HTTP function. The name becomes its service's name.
func HTTP(name string, h http.Handler, opts ...Option) {
	if h == nil {
		panic("functions: HTTP " + name + ": nil handler")
	}
	register(&function{name: name, kind: kindHTTP, http: h}, opts)
}

// HTTPFunc registers an HTTP function from a handler function.
func HTTPFunc(name string, f func(http.ResponseWriter, *http.Request), opts ...Option) {
	HTTP(name, http.HandlerFunc(f), opts...)
}

// Schedule registers a function run on a five-field cron schedule, e.g.
// "0 3 * * *". A run that returns an error exits 1.
func Schedule(name, schedule string, f func(ctx context.Context) error, opts ...Option) {
	if len(strings.Fields(schedule)) != 5 {
		panic(fmt.Sprintf("functions: Schedule %s: %q isn't a five-field cron schedule", name, schedule))
	}
	register(&function{name: name, kind: kindSchedule, schedule: schedule, run: f}, opts)
}

// Queue registers a worker for a queue. Add jobs with Enqueue. A job whose
// handler returns an error, panics or passes its timeout is retried with
// backoff; one whose worker died is claimed again once its lease runs out.
// Jobs can run more than once, so make handlers safe to repeat, and let them
// return when ctx is done.
func Queue(name, queue string, f func(ctx context.Context, job *Job) error, opts ...Option) {
	checkQueue(queue)
	register(&function{name: name, kind: kindQueue, queue: queue, job: f}, opts)
}

func checkQueue(queue string) {
	if !queueRe.MatchString(queue) {
		panic(fmt.Sprintf("functions: queue name %q: use lowercase letters, digits, dashes and underscores", queue))
	}
}

// reset empties the registry (tests).
func reset() {
	mu.Lock()
	defer mu.Unlock()
	registry = map[string]*function{}
	migrationsFS = nil
}
