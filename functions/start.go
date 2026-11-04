package functions

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Start runs the function named in $FUNCTION_TARGET (or --target) and exits
// when it's done: an HTTP function serves on $PORT until SIGTERM, a schedule
// runs once, a queue worker runs until SIGTERM. On SIGTERM it finishes what's
// in flight and exits 0, so pods don't end in Error.
//
// With --describe, or MAXCLOUD_DESCRIBE=1 in the environment, it prints the
// registered functions as JSON instead, which is how mcloud deploys them.
func Start() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run is Start without the process around it, for tests. It returns the
// exit code.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("functions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	describeFlag := fs.Bool("describe", false, "print the functions as JSON and exit")
	target := fs.String("target", getenv("FUNCTION_TARGET"), "function to run (default $FUNCTION_TARGET)")
	port := fs.String("port", or(getenv("PORT"), "8080"), "port for an HTTP function (default $PORT or 8080)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags)

	switch {
	case *describeFlag:
		out, _ := json.MarshalIndent(describe(), "", "  ")
		fmt.Fprintf(stdout, "%s\n", out)
		return 0
	case getenv("MAXCLOUD_DESCRIBE") == "1":
		out, _ := json.Marshal(describe())
		fmt.Fprintf(stdout, "%s%s\n", describeTag, out)
		return 0
	}

	mu.Lock()
	f := registry[*target]
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	mu.Unlock()
	sort.Strings(names)
	if f == nil {
		if *target == "" {
			fmt.Fprintf(stderr, "functions: which function? Set FUNCTION_TARGET or --target (%s)\n", strings.Join(names, ", "))
		} else {
			fmt.Fprintf(stderr, "functions: no function %s (found: %s)\n", *target, strings.Join(names, ", "))
		}
		return 2
	}

	switch f.kind {
	case kindHTTP:
		if err := serveHTTP(ctx, f, ":"+*port, logger, nil); err != nil {
			logger.Printf("%s: %v", f.name, err)
			return 1
		}
		return 0
	case kindSchedule:
		return runSchedule(ctx, f, logger)
	default:
		p, err := Pool(ctx)
		if err != nil {
			logger.Print(err)
			return 1
		}
		if err := runWorker(ctx, f, workerConfig{pool: p, logf: logger.Printf}); err != nil {
			logger.Printf("%s: %v", f.name, err)
			return 1
		}
		return 0
	}
}

// serveHTTP serves until ctx is done, then stops taking requests and waits
// up to 25 s for those in flight. ready, if set, gets the listener's address.
func serveHTTP(ctx context.Context, f *function, addr string, logger *log.Logger, ready chan<- string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           recoverer(f.http, logger),
		ReadHeaderTimeout: 30 * time.Second,
		// Knative's queue-proxy keeps connections open; let it reuse them.
		IdleTimeout: 65 * time.Second,
		ErrorLog:    logger,
	}
	logger.Printf("%s: listening on %s", f.name, ln.Addr())
	if ready != nil {
		ready <- ln.Addr().String()
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Printf("%s: shutting down", f.name)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		srv.Close()
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// recoverer turns a panic into a 500 instead of a dropped connection.
func recoverer(h http.Handler, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				logger.Printf("%s %s: panic: %v", r.Method, r.URL.Path, v)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		h.ServeHTTP(w, r)
	})
}

func runSchedule(ctx context.Context, f *function, logger *log.Logger) int {
	timeout := time.Duration(f.timeoutSeconds(defaultScheduleTimeout)) * time.Second
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("timed out after %s", timeout))
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- fmt.Errorf("panic: %v", v)
			}
		}()
		done <- f.run(ctx)
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = context.Cause(ctx)
	}
	if err != nil {
		logger.Printf("%s: failed: %v", f.name, err)
		return 1
	}
	logger.Printf("%s: done in %.1fs", f.name, time.Since(start).Seconds())
	return 0
}
