package cli

import (
	"bytes"
	"strings"
	"testing"
)

func render(lines ...string) (out, progress string, r *promptRender) {
	var o, p bytes.Buffer
	r = &promptRender{out: &o, progress: &p, dim: func(s string) string { return s }}
	// In two writes that split a line, as a stream does.
	all := strings.Join(lines, "\n") + "\n"
	r.Write([]byte(all[:len(all)/2]))
	r.Write([]byte(all[len(all)/2:]))
	r.Close()
	return o.String(), p.String(), r
}

// What the agent does shows as progress; its answer, once, on stdout.
func TestPromptRender(t *testing.T) {
	out, progress, r := render(
		`{"type":"system","subtype":"init","permissionMode":"auto"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll run the tests."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"npm test\n  --silent","description":"Run tests"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":""}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"All 12 tests pass."}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"All 12 tests pass.","num_turns":2,"duration_ms":72400}`,
	)
	if out != "All 12 tests pass.\n" {
		t.Errorf("out = %q", out)
	}
	if want := "I'll run the tests.\n→ Bash  npm test --silent\ndone in 1m12s, 2 turns\n"; progress != want {
		t.Errorf("progress = %q", progress)
	}
	if r.failed != "" {
		t.Errorf("failed = %q", r.failed)
	}
}

// An error ends the command with it, and what isn't an event is shown as
// it is: claude's own complaints come that way.
func TestPromptRenderErrors(t *testing.T) {
	out, progress, r := render(
		`Failed to authenticate. API Error: 401`,
		`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":3}`,
	)
	if out != "" || !strings.Contains(progress, "Failed to authenticate") || r.failed != "error_max_turns" {
		t.Errorf("out %q, progress %q, failed %q", out, progress, r.failed)
	}
	// No result at all: what the agent said last isn't lost.
	_, progress, _ = render(`{"type":"assistant","message":{"content":[{"type":"text","text":"Working on it."}]}}`)
	if progress != "Working on it.\n" {
		t.Errorf("progress = %q", progress)
	}
}

func TestToolSummary(t *testing.T) {
	if got := toolSummary(map[string]any{"file_path": "/work/a.go", "old_string": "x"}); got != "/work/a.go" {
		t.Errorf("%q", got)
	}
	if got := toolSummary(map[string]any{"command": strings.Repeat("x", 300)}); len([]rune(got)) != 100 {
		t.Errorf("%d runes", len([]rune(got)))
	}
	if got := toolSummary(map[string]any{"todos": []any{}}); got != "" {
		t.Errorf("%q", got)
	}
}
