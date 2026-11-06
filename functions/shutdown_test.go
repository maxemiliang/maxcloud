package functions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOnShutdownRunsHooksInOrder(t *testing.T) {
	t.Cleanup(reset)
	Schedule("ok", "* * * * *", func(context.Context) error { return nil })
	var ran []string
	OnShutdown(func(context.Context) error { ran = append(ran, "pool"); return nil })
	OnShutdown(func(context.Context) error { ran = append(ran, "logs"); return nil })
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"--target", "ok"}, env(nil), io.Discard, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Join(ran, ",") != "pool,logs" {
		t.Errorf("ran %v", ran)
	}
	// Run once: a second run (a test) doesn't run them again.
	ran = nil
	run(context.Background(), []string{"--target", "ok"}, env(nil), io.Discard, io.Discard)
	if len(ran) != 0 {
		t.Errorf("ran again: %v", ran)
	}
}

func TestOnShutdownFailureExits1(t *testing.T) {
	t.Cleanup(reset)
	Schedule("ok", "* * * * *", func(context.Context) error { return nil })
	Schedule("bad", "* * * * *", func(context.Context) error { return errors.New("report failed") })
	for _, tc := range []struct {
		name, target string
		hook         func(context.Context) error
		want         int
		log          string
	}{
		{"error", "ok", func(context.Context) error { return errors.New("flush failed") }, 1, "flush failed"},
		{"panic", "ok", func(context.Context) error { panic("oops") }, 1, "oops"},
		{"the function's code stays", "bad", func(context.Context) error { return errors.New("x") }, 1, "x"},
	} {
		var later bool
		OnShutdown(tc.hook)
		OnShutdown(func(context.Context) error { later = true; return nil })
		var errOut bytes.Buffer
		if code := run(context.Background(), []string{"--target", tc.target}, env(nil), io.Discard, &errOut); code != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, code, tc.want)
		}
		if !strings.Contains(errOut.String(), tc.log) {
			t.Errorf("%s: stderr %q", tc.name, errOut.String())
		}
		if !later {
			t.Errorf("%s: the hook after the failed one didn't run", tc.name)
		}
	}
}

func TestOnShutdownOverrunExits1(t *testing.T) {
	t.Cleanup(reset)
	defer func(d time.Duration) { shutdownTimeout = d }(shutdownTimeout)
	shutdownTimeout = 100 * time.Millisecond
	Schedule("ok", "* * * * *", func(context.Context) error { return nil })
	var ctxDone atomic.Bool
	OnShutdown(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			ctxDone.Store(true)
		case <-time.After(5 * time.Second):
		}
		time.Sleep(time.Second) // ignores its context
		return nil
	})
	start := time.Now()
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"--target", "ok"}, env(nil), io.Discard, &errOut); code != 1 {
		t.Errorf("exit %d", code)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("waited %s for an overrunning hook", d)
	}
	if !strings.Contains(errOut.String(), "still running") {
		t.Errorf("stderr %q", errOut.String())
	}
	time.Sleep(50 * time.Millisecond)
	if !ctxDone.Load() {
		t.Error("the hook's context wasn't cancelled at the deadline")
	}
}
