package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSplitSessionPath(t *testing.T) {
	for _, c := range []struct {
		arg, session, path string
		remote             bool
	}{
		{"work:repo", "work", "repo", true},
		{"work:", "work", "", true},
		{"work:/tmp/x", "work", "/tmp/x", true},
		{"repo", "", "repo", false},
		{"./odd:name", "", "./odd:name", false},
		{"dir/a:b", "", "dir/a:b", false},
		{"notes.md:1", "", "notes.md:1", false},
		{":x", "", ":x", false},
	} {
		session, path, remote := splitSessionPath(c.arg)
		if session != c.session || path != c.path || remote != c.remote {
			t.Errorf("%q: %q %q %v", c.arg, session, path, remote)
		}
	}
}

// What follows -- is the command's, whether or not a session is named.
func TestSessionAndArgs(t *testing.T) {
	for _, c := range []struct {
		args    string
		session string
		rest    string
		fails   bool
	}{
		{"", "", "", false},
		{"work", "work", "", false},
		{"-- -p hi", "", "-p hi", false},
		{"work -- --continue", "work", "--continue", false},
		{"work other", "", "", true},
	} {
		var session string
		var rest []string
		var err error
		cmd := &cobra.Command{Use: "x", RunE: func(cmd *cobra.Command, args []string) error {
			session, rest, err = sessionAndArgs(cmd, args)
			return nil
		}}
		cmd.SetArgs(strings.Fields(c.args))
		if execErr := cmd.Execute(); execErr != nil {
			t.Fatalf("%q: %v", c.args, execErr)
		}
		if (err != nil) != c.fails || session != c.session || strings.Join(rest, " ") != c.rest {
			t.Errorf("%q: %q %q %v", c.args, session, rest, err)
		}
	}
}
