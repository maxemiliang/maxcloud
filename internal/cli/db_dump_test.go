package cli

import (
	"strings"
	"testing"
)

func TestDumpCommand(t *testing.T) {
	cmd, err := dumpCommand("plain", false, false)
	if err != nil || strings.Join(cmd, " ") != "pg_dump -d app --no-owner --no-privileges" {
		t.Errorf("plain: %v, %v", cmd, err)
	}
	cmd, err = dumpCommand("custom", true, false)
	if err != nil || strings.Join(cmd, " ") != "pg_dump -d app --no-owner --no-privileges --format=custom --schema-only" {
		t.Errorf("custom, schema only: %v, %v", cmd, err)
	}
	if _, err := dumpCommand("tar", false, false); err == nil {
		t.Error("--format tar: no error")
	}
	if _, err := dumpCommand("plain", true, true); err == nil {
		t.Error("--schema-only --data-only: no error")
	}
}

// An import runs as the app's role and in one transaction, whichever tool
// reads the dump.
func TestImportCommand(t *testing.T) {
	sql := strings.Join(importCommand(false), " ")
	for _, want := range []string{"psql", "ON_ERROR_STOP=1", "--single-transaction", "-c SET ROLE app", "-f -"} {
		if !strings.Contains(sql, want) {
			t.Errorf("psql import %q: no %q", sql, want)
		}
	}
	archive := strings.Join(importCommand(true), " ")
	for _, want := range []string{"pg_restore", "--role=app", "--single-transaction", "--exit-on-error", "--no-owner"} {
		if !strings.Contains(archive, want) {
			t.Errorf("pg_restore import %q: no %q", archive, want)
		}
	}
	if !isCustomDump([]byte("PGDMP\x01\x0e")) || isCustomDump([]byte("--\n-- PostgreSQL database dump")) || isCustomDump(nil) {
		t.Error("isCustomDump")
	}
}
