package functions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var failNightly atomic.Bool

func registerDevApp(t *testing.T) {
	t.Helper()
	reset()
	t.Cleanup(reset)
	HTTPFunc("api-server", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "api %s %s %s %s", r.Method, r.URL.RequestURI(), r.Header.Get("X-In"), body)
	})
	HTTPFunc("web", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "web") })
	Schedule("nightly", "0 3 * * *", func(context.Context) error {
		if failNightly.Load() {
			return errors.New("nightly broke")
		}
		return nil
	})
	Queue("resize", "images", func(context.Context, *Job) error { return nil })
}

// startDev runs dev mode until the test ends, and returns its port and the
// startup table.
func startDev(t *testing.T, getenv func(string) string) (int, string) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	ready := make(chan int, 1)
	table := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- runDev(ctx, devConfig{addr: "127.0.0.1:0", getenv: getenv, logger: log.New(io.Discard, "", 0), stderr: table, ready: ready})
	}()
	var port int
	select {
	case port = <-ready:
	case code := <-done:
		t.Fatalf("runDev exited %d", code)
	}
	t.Cleanup(func() {
		stop()
		if code := <-done; code != 0 {
			t.Errorf("runDev exited %d", code)
		}
	})
	return port, table.String()
}

type devResponse struct {
	status int
	body   string
	ctype  string
}

func devDo(t *testing.T, port int, method, host, path, body string, header ...string) devResponse {
	t.Helper()
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return devResponse{res.StatusCode, string(b), res.Header.Get("Content-Type")}
}

func TestHostOnly(t *testing.T) {
	for in, want := range map[string]string{"Api.Localhost:8080": "api.localhost", "localhost": "localhost", "[::1]:8080": "[::1]", "": ""} {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDevRoutesByHost(t *testing.T) {
	registerDevApp(t)
	port, _ := startDev(t, env(nil))
	got := devDo(t, port, "POST", fmt.Sprintf("api-server.localhost:%d", port), "/a/b?c=1", "body", "X-In", "yes")
	if got.body != "api POST /a/b?c=1 yes body" {
		t.Errorf("api-server: %q", got.body)
	}
	if got := devDo(t, port, "GET", "web.localhost", "/", ""); got.body != "web" {
		t.Errorf("web: %q", got.body)
	}
	if got := devDo(t, port, "GET", "nope.localhost", "/", ""); got.status != 404 || !strings.Contains(got.body, "no HTTP function nope") {
		t.Errorf("nope: %+v", got)
	}
	if got := devDo(t, port, "GET", "nightly.localhost", "/", ""); got.status != 404 {
		t.Errorf("only HTTP functions have a host: %+v", got)
	}
}

func TestDevListing(t *testing.T) {
	registerDevApp(t)
	port, _ := startDev(t, env(nil))
	want := []devListing{
		{Kind: kindHTTP, Name: "api-server", URL: fmt.Sprintf("http://api-server.localhost:%d/", port)},
		{Kind: kindSchedule, Name: "nightly"},
		{Kind: kindQueue, Name: "resize"},
		{Kind: kindHTTP, Name: "web", URL: fmt.Sprintf("http://web.localhost:%d/", port)},
	}
	for _, host := range []string{fmt.Sprintf("localhost:%d", port), fmt.Sprintf("127.0.0.1:%d", port)} {
		res := devDo(t, port, "GET", host, "/", "")
		if res.status != 200 || !strings.Contains(res.ctype, "json") {
			t.Fatalf("%s: %+v", host, res)
		}
		var got []devListing
		if err := json.Unmarshal([]byte(res.body), &got); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %+v, want %+v", host, got, want)
		}
	}
	// The same JSON as the Node.js SDK: no url key for the others.
	if res := devDo(t, port, "GET", "localhost", "/", ""); !strings.Contains(res.body, `{"kind":"schedule","name":"nightly"}`) {
		t.Errorf("listing: %s", res.body)
	}
}

func TestDevRunSchedule(t *testing.T) {
	registerDevApp(t)
	port, _ := startDev(t, env(nil))
	if got := devDo(t, port, "POST", "localhost", "/_run/nightly", ""); got.status != 200 || got.body != "ok\n" {
		t.Errorf("run: %+v", got)
	}
	failNightly.Store(true)
	t.Cleanup(func() { failNightly.Store(false) })
	if got := devDo(t, port, "POST", "localhost", "/_run/nightly", ""); got.status != 500 || got.body != "nightly broke\n" {
		t.Errorf("failing run: %+v", got)
	}
	for _, name := range []string{"nope", "web", "resize"} {
		if got := devDo(t, port, "POST", "localhost", "/_run/"+name, ""); got.status != 404 || !strings.Contains(got.body, "no scheduled function "+name) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestDevNotFound(t *testing.T) {
	registerDevApp(t)
	port, _ := startDev(t, env(nil))
	hint := fmt.Sprintf("GET http://localhost:%d/ lists the functions", port)
	for _, mp := range [][2]string{{"GET", "/other"}, {"POST", "/"}, {"GET", "/_run/nightly"}} {
		if got := devDo(t, port, mp[0], "localhost", mp[1], ""); got.status != 404 || !strings.Contains(got.body, hint) {
			t.Errorf("%s %s: %+v", mp[0], mp[1], got)
		}
	}
}

func TestDevWithoutDatabase(t *testing.T) {
	registerDevApp(t)
	port, table := startDev(t, env(nil))
	for _, re := range []string{
		fmt.Sprintf(`http +api-server +http://api-server\.localhost:%d/`, port),
		fmt.Sprintf(`schedule +nightly +POST http://localhost:%d/_run/nightly`, port),
		`queue +resize +not running \(no DATABASE_URL\)`,
		`queue workers don't run without DATABASE_URL \(mcloud dev provides one\)`,
	} {
		if !regexp.MustCompile(re).MatchString(table) {
			t.Errorf("table lacks %s:\n%s", re, table)
		}
	}
	if got := devDo(t, port, "GET", "web.localhost", "/", ""); got.body != "web" {
		t.Errorf("web: %q", got.body)
	}
}

func TestDevRunsQueueWorkers(t *testing.T) {
	testPool(t) // sets DATABASE_URL
	reset()
	t.Cleanup(reset)
	q := queueName()
	got := make(chan string, 1)
	Queue("work", q, func(_ context.Context, j *Job) error {
		got <- string(j.Payload)
		return nil
	})
	_, table := startDev(t, os.Getenv)
	if !regexp.MustCompile(`queue +work +worker running`).MatchString(table) {
		t.Errorf("table:\n%s", table)
	}
	if _, err := Enqueue(context.Background(), q, map[string]int{"n": 7}); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-got:
		if payload != `{"n": 7}` && payload != `{"n":7}` {
			t.Errorf("payload %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker didn't run the job")
	}
}

func TestProcessDevMode(t *testing.T) {
	bin := buildApp(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MAXCLOUD_DEV=1", "PORT=0", "DATABASE_URL=")
	logs := &syncBuffer{}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`http://localhost:(\d+)/ lists`)
	var port int
	for deadline := time.Now().Add(5 * time.Second); port == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m := re.FindStringSubmatch(logs.String()); m != nil {
			fmt.Sscan(m[1], &port)
		}
	}
	if port == 0 {
		cmd.Process.Kill()
		t.Fatalf("not listening: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "not running (no DATABASE_URL)") {
		t.Errorf("table: %s", logs.String())
	}
	body := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/?wait=300", port), nil)
		req.Host = "hello.localhost"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			body <- err.Error()
			return
		}
		b, _ := io.ReadAll(res.Body)
		body <- string(b)
	}()
	time.Sleep(100 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGTERM)
	if got := <-body; got != "hello\n" {
		t.Errorf("in-flight request got %q", got)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("exit: %v\n%s", err, logs.String())
	}
}
