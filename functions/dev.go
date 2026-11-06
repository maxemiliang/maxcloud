package functions

// Dev mode: the whole app in one process, for local development (--dev or
// MAXCLOUD_DEV=1, which mcloud dev uses). One HTTP server on 127.0.0.1
// routes <name>.localhost to the HTTP function registered as <name>; on
// localhost itself, GET / lists the functions and POST /_run/<name> runs a
// schedule once. Queue workers run in-process when $DATABASE_URL is set.
// Schedules never run on their cron here.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type devConfig struct {
	addr   string // host:port to listen on; port 0 picks one
	getenv func(string) string
	logger *log.Logger
	stderr io.Writer  // where the startup table goes
	ready  chan<- int // gets the port once it's listening (tests)
}

// devListing is one entry in GET /'s listing.
type devListing struct {
	Kind kind   `json:"kind"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// devFunctions is the registry, sorted by name.
func devFunctions() []*function {
	mu.Lock()
	defer mu.Unlock()
	fs := make([]*function, 0, len(registry))
	for _, f := range registry {
		fs = append(fs, f)
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].name < fs[j].name })
	return fs
}

// hostOnly is a Host header's hostname, lowercase and without the port.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
		if strings.Contains(h, ":") {
			host = "[" + h + "]"
		}
	}
	return strings.ToLower(strings.TrimSpace(host))
}

// devHandler routes dev mode's requests.
func devHandler(fs []*function, port int, logger *log.Logger) http.Handler {
	root := fmt.Sprintf("http://localhost:%d/", port)
	byName := map[string]*function{}
	listing := make([]devListing, 0, len(fs))
	for _, f := range fs {
		byName[f.name] = f
		l := devListing{Kind: f.kind, Name: f.name}
		if f.kind == kindHTTP {
			l.URL = fmt.Sprintf("http://%s.localhost:%d/", f.name, port)
		}
		listing = append(listing, l)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if name, ok := strings.CutSuffix(host, ".localhost"); ok {
			f := byName[name]
			if f == nil || f.kind != kindHTTP {
				http.Error(w, fmt.Sprintf("no HTTP function %s: GET %s lists the functions", name, root), http.StatusNotFound)
				return
			}
			f.http.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(listing)
			return
		}
		if name, ok := strings.CutPrefix(r.URL.Path, "/_run/"); ok && r.Method == http.MethodPost && name != "" && !strings.Contains(name, "/") {
			f := byName[name]
			if f == nil || f.kind != kindSchedule {
				http.Error(w, fmt.Sprintf("no scheduled function %s: GET %s lists the functions", name, root), http.StatusNotFound)
				return
			}
			if err := runScheduleLogged(r.Context(), f, logger); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "ok")
			return
		}
		http.Error(w, fmt.Sprintf("not found: GET %s lists the functions, POST %s_run/<name> runs a scheduled one", root, root), http.StatusNotFound)
	})
}

// devTable is the startup table: one row per function, and how to reach it.
func devTable(fs []*function, port int, workersRunning bool) string {
	var rows [][3]string
	kindW, nameW := 0, 0
	hasQueue := false
	for _, f := range fs {
		var where string
		switch f.kind {
		case kindHTTP:
			where = fmt.Sprintf("http://%s.localhost:%d/", f.name, port)
		case kindSchedule:
			where = fmt.Sprintf("POST http://localhost:%d/_run/%s", port, f.name)
		case kindQueue:
			hasQueue = true
			where = "not running (no DATABASE_URL)"
			if workersRunning {
				where = "worker running"
			}
		}
		rows = append(rows, [3]string{string(f.kind), f.name, where})
		kindW, nameW = max(kindW, len(f.kind)), max(nameW, len(f.name))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "maxcloud dev: http://localhost:%d/ lists the functions\n", port)
	if len(rows) == 0 {
		b.WriteString("  (no functions found)\n")
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-*s  %-*s  %s\n", kindW, r[0], nameW, r[1], r[2])
	}
	if hasQueue && !workersRunning {
		b.WriteString("queue workers don't run without DATABASE_URL (mcloud dev provides one)\n")
	}
	return b.String()
}

// runDev runs dev mode until ctx is done, then lets requests and jobs in
// flight finish. It returns the exit code.
func runDev(ctx context.Context, cfg devConfig) int {
	fs := devFunctions()
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		cfg.logger.Printf("maxcloud dev: %v", err)
		return 1
	}
	port := ln.Addr().(*net.TCPAddr).Port

	var workers sync.WaitGroup
	hasDatabase := cfg.getenv("DATABASE_URL") != ""
	if hasDatabase {
		for _, f := range fs {
			if f.kind != kindQueue {
				continue
			}
			workers.Go(func() {
				if err := runWorker(ctx, f, workerConfig{url: cfg.getenv("DATABASE_URL"), logf: cfg.logger.Printf}); err != nil {
					cfg.logger.Printf("%s: worker stopped: %v", f.name, err)
				}
			})
		}
	}

	fmt.Fprint(cfg.stderr, devTable(fs, port, hasDatabase))
	if cfg.ready != nil {
		cfg.ready <- port
	}
	err = serve(ctx, "maxcloud dev", devHandler(fs, port, cfg.logger), ln, cfg.logger)
	workers.Wait()
	if err != nil {
		cfg.logger.Printf("maxcloud dev: %v", err)
		return 1
	}
	return 0
}
