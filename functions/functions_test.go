package functions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRegisterValidates(t *testing.T) {
	t.Cleanup(reset)
	for name, f := range map[string]func(){
		"bad name":      func() { HTTPFunc("Hello", func(http.ResponseWriter, *http.Request) {}) },
		"bad schedule":  func() { Schedule("x", "daily", func(context.Context) error { return nil }) },
		"bad queue":     func() { Queue("x", "Images!", func(context.Context, *Job) error { return nil }) },
		"nil handler":   func() { HTTP("x", nil) },
		"registered 2x": func() { HTTPFunc("dup", nil); HTTPFunc("dup", nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: want a panic", name)
				}
			}()
			f()
		}()
	}
}

func TestDescribe(t *testing.T) {
	t.Cleanup(reset)
	HTTPFunc("api", func(http.ResponseWriter, *http.Request) {}, Public(), Memory("256Mi"), Concurrency(10))
	Schedule("nightly", "0 3 * * *", func(context.Context) error { return nil }, TimeZone("Europe/Helsinki"), Timeout(10*time.Minute))
	Queue("resize", "images", func(context.Context, *Job) error { return nil }, Concurrency(2), Backoff(30*time.Second))

	var out bytes.Buffer
	if code := run(context.Background(), nil, env(map[string]string{"MAXCLOUD_DESCRIBE": "1"}), &out, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	line := strings.TrimSpace(out.String())
	if !strings.HasPrefix(line, describeTag) {
		t.Fatalf("no tag: %q", line)
	}
	var d struct {
		SDK       string           `json:"sdk"`
		Functions []map[string]any `json:"functions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, describeTag)), &d); err != nil {
		t.Fatal(err)
	}
	if d.SDK != modulePath || len(d.Functions) != 3 {
		t.Fatalf("%+v", d)
	}
	api, nightly, resize := d.Functions[0], d.Functions[1], d.Functions[2]
	want := map[string]any{"name": "api", "export": "api", "kind": "http", "memory": "256Mi", "cpu": nil, "public": true,
		"minInstances": 0.0, "maxInstances": 3.0, "concurrency": 10.0, "timeoutSeconds": 300.0}
	for k, v := range want {
		if api[k] != v {
			t.Errorf("api %s = %v, want %v", k, api[k], v)
		}
	}
	if nightly["timeZone"] != "Europe/Helsinki" || nightly["timeoutSeconds"] != 600.0 || nightly["retries"] != 0.0 {
		t.Errorf("nightly %v", nightly)
	}
	if resize["queue"] != "images" || resize["concurrency"] != 2.0 || resize["backoffSeconds"] != 30.0 || resize["maxAttempts"] != 5.0 ||
		!strings.Contains(resize["backlogQuery"].(string), "queue = 'images'") {
		t.Errorf("resize %v", resize)
	}
}

func TestUnknownTarget(t *testing.T) {
	t.Cleanup(reset)
	HTTPFunc("hello", func(http.ResponseWriter, *http.Request) {})
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"--target", "nope"}, env(nil), io.Discard, &errOut); code != 2 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "found: hello") {
		t.Errorf("stderr %q", errOut.String())
	}
}

func TestScheduleExitCodes(t *testing.T) {
	t.Cleanup(reset)
	Schedule("ok", "* * * * *", func(context.Context) error { return nil })
	Schedule("bad", "* * * * *", func(context.Context) error { return errors.New("report failed") })
	Schedule("boom", "* * * * *", func(context.Context) error { panic("oops") })
	Schedule("slow", "* * * * *", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, Timeout(100*time.Millisecond))
	for target, want := range map[string]int{"ok": 0, "bad": 1, "boom": 1, "slow": 1} {
		var errOut bytes.Buffer
		if code := run(context.Background(), []string{"--target", target}, env(nil), io.Discard, &errOut); code != want {
			t.Errorf("%s: exit %d, want %d (%s)", target, code, want, errOut.String())
		}
	}
}

func TestHTTPDrainsOnStop(t *testing.T) {
	t.Cleanup(reset)
	f := &function{name: "web", kind: kindHTTP, http: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panic" {
			panic("boom")
		}
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte("finished"))
	})}
	ctx, stop := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	served := make(chan error, 1)
	go func() { served <- serveHTTP(ctx, f, "127.0.0.1:0", log.New(io.Discard, "", 0), ready) }()
	addr := <-ready

	res, err := http.Get("http://" + addr + "/panic")
	if err != nil || res.StatusCode != 500 {
		t.Fatalf("panic: %v %v", res, err)
	}
	body := make(chan string, 1)
	go func() {
		res, err := http.Get("http://" + addr + "/")
		if err != nil {
			body <- err.Error()
			return
		}
		b, _ := io.ReadAll(res.Body)
		body <- string(b)
	}()
	time.Sleep(100 * time.Millisecond)
	stop()
	if got := <-body; got != "finished" {
		t.Errorf("in-flight request: %q", got)
	}
	if err := <-served; err != nil {
		t.Errorf("serve: %v", err)
	}
	if _, err := http.Get("http://" + addr + "/"); err == nil {
		t.Error("still serving after stop")
	}
}
