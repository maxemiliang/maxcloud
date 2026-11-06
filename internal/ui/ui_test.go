package ui

import (
	"errors"
	"strings"
	"testing"
)

// Off a terminal everything is plain: no escape codes, the old prefixes.
func TestPlain(t *testing.T) {
	var out, errOut strings.Builder
	u := New(strings.NewReader(""), &out, &errOut, false)
	if u.Fancy() {
		t.Fatal("fancy without a terminal")
	}
	s := u.StepTail("Building", 3)
	s.Status("queued")
	s.Status("queued")
	s.Writer().Write([]byte("line 1\r\nline 2\n"))
	s.Done("")
	u.Warn("careful %d", 1)
	u.Note("fyi")
	u.Success("did %s", "it")
	u.Error(errors.New("broke\nhint"))
	u.Table([]string{"Name", "State"}, [][]string{{"a", "Ready"}, {"longer", "x"}})

	wantOut := "Building ...\n  queued\n  line 1\n  line 2\n  done (0s)\n✓ did it\nNAME    STATE\na       Ready\nlonger  x\n"
	if got := out.String(); got != wantOut {
		t.Errorf("out:\n%q\nwant\n%q", got, wantOut)
	}
	if got := errOut.String(); got != "warning: careful 1\nnote: fyi\nmcloud: broke\nhint\n" {
		t.Errorf("err: %q", got)
	}
	if strings.Contains(out.String()+errOut.String(), "\x1b") {
		t.Error("escape codes off a terminal")
	}
}

func TestTableAlignsColoredCells(t *testing.T) {
	var out strings.Builder
	u := &UI{Out: &out, Err: &out, fancy: true}
	u.Table([]string{"A", "B"}, [][]string{{u.Green("ok"), "1"}, {"longer", "2"}})
	lines := strings.Split(strings.TrimSpace(stripANSI(out.String())), "\n")
	if len(lines) != 3 || lines[1] != "ok      1" || lines[2] != "longer  2" {
		t.Errorf("%q", lines)
	}
}

func TestFit(t *testing.T) {
	if fit("abcdef", 4) != "abc…" || fit("abc", 4) != "abc" {
		t.Error(fit("abcdef", 4))
	}
}
