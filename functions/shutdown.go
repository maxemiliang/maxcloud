package functions

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

var (
	shutdownMu    sync.Mutex
	shutdownHooks []func(context.Context) error
	// shutdownTimeout is how long the hooks get, together.
	shutdownTimeout = 10 * time.Second
)

// OnShutdown runs fn when the function stops (after SIGTERM, once the
// requests or jobs in flight are done; after a schedule's run), before the
// process exits: close a pool, flush logs. Start exits with os.Exit, so
// deferred calls in main never run; this is where that cleanup goes.
//
// Hooks run in the order they were registered, for at most 10 seconds
// together; ctx ends at that deadline. A hook that returns an error or
// panics is logged and the rest still run. If one failed, or they're still
// running at the deadline, the process exits 1 (or with the function's own
// non-zero code).
func OnShutdown(fn func(ctx context.Context) error) {
	if fn == nil {
		panic("functions: OnShutdown(nil)")
	}
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	shutdownHooks = append(shutdownHooks, fn)
}

// runShutdownHooks runs the hooks registered so far (each once) and
// reports whether they all succeeded in time.
func runShutdownHooks(ctx context.Context, logger *log.Logger) bool {
	shutdownMu.Lock()
	hooks := shutdownHooks
	shutdownHooks = nil
	shutdownMu.Unlock()
	if len(hooks) == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		ok := true
		for _, h := range hooks {
			if err := callHook(ctx, h); err != nil {
				ok = false
				logger.Printf("shutdown hook failed: %v", err)
			}
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		return ok
	case <-ctx.Done():
		logger.Printf("shutdown hooks still running after %s; exiting", shutdownTimeout)
		return false
	}
}

func callHook(ctx context.Context, h func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return h(ctx)
}
