package functions

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe to read while os/exec writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The test app as a real process: what the container runs.
func buildApp(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "app")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/app").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestProcessSIGTERM(t *testing.T) {
	bin := buildApp(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "FUNCTION_TARGET=hello", "PORT=0")
	logs := &syncBuffer{}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`listening on (\S+)`)
	var addr string
	for deadline := time.Now().Add(5 * time.Second); addr == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m := re.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
		}
	}
	if addr == "" {
		t.Fatalf("not listening: %s", logs.String())
	}
	body := make(chan string, 1)
	go func() {
		res, err := http.Get("http://" + addr + "/?wait=300")
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

func TestProcessSchedule(t *testing.T) {
	bin := buildApp(t)
	for env, want := range map[string]int{"": 0, "FAIL=1": 1} {
		cmd := exec.Command(bin, "--target", "nightly-report")
		cmd.Env = append(os.Environ(), env)
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if code != want {
			t.Errorf("%q: exit %d, want %d", env, code, want)
		}
	}
}
