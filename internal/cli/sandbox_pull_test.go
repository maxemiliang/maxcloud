package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/maxemiliang/maxcloud/internal/kube"
)

// git runs git in dir for a test, as a fixed author.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitIn(context.Background(), dir, append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func commitFile(t *testing.T, dir, name, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", name)
	testGit(t, dir, "commit", "-q", "-m", msg)
}

// The session's side of a pull, run here: the same command, in a
// directory standing in for the session's.
func localBundle(t *testing.T, dir string) func(context.Context, []string, io.Writer) error {
	return func(ctx context.Context, have []string, w io.Writer) error {
		argv := kube.BundleCommand(dir, have)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = w, os.Stderr
		return cmd.Run()
	}
}

func TestPullCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ctx := context.Background()
	mine := filepath.Join(t.TempDir(), "mine")
	testGit(t, "", "init", "-q", "-b", "main", mine)
	commitFile(t, mine, "a.txt", "first")

	// The session starts as a copy, then works: two commits on main, one
	// on a branch of its own, and something it didn't commit.
	theirs := filepath.Join(t.TempDir(), "theirs")
	testGit(t, "", "clone", "-q", mine, theirs)
	commitFile(t, theirs, "b.txt", "second")
	commitFile(t, theirs, "c.txt", "third")
	testGit(t, theirs, "switch", "-q", "-c", "idea")
	commitFile(t, theirs, "d.txt", "an idea")
	os.WriteFile(filepath.Join(theirs, "loose.txt"), []byte("x"), 0o644)

	got, err := pullCommits(ctx, mine, "work", localBundle(t, theirs))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]pulledBranch{}
	for _, b := range got {
		byName[b.Name] = b
	}
	if len(got) != 2 || byName["main"].New != 2 || byName["idea"].New != 3 || byName["main"].Ref != "sandbox/work/main" {
		t.Fatalf("%+v", got)
	}
	if tip := testGit(t, mine, "rev-parse", "refs/remotes/sandbox/work/main"); tip != testGit(t, theirs, "rev-parse", "main") {
		t.Errorf("sandbox/work/main is at %s", tip)
	}
	// Fetched, not merged, and nothing of the working tree came along.
	if _, err := os.Stat(filepath.Join(mine, "b.txt")); err == nil {
		t.Error("pullCommits changed the working tree")
	}
	if tags := testGit(t, mine, "tag"); tags != "" {
		t.Errorf("tags: %q", tags)
	}

	// Again, with nothing new: nothing is sent.
	if got, err := pullCommits(ctx, mine, "work", localBundle(t, theirs)); err != nil || len(got) != 0 {
		t.Errorf("second pull: %+v, %v", got, err)
	}
	// One more commit: only that branch comes.
	commitFile(t, theirs, "e.txt", "more of the idea")
	got, err = pullCommits(ctx, mine, "work", localBundle(t, theirs))
	if err != nil || len(got) != 1 || got[0].Name != "idea" || got[0].New != 4 {
		t.Errorf("third pull: %+v, %v", got, err)
	}
}

// Not a repository in the session: said, not a broken bundle.
func TestPullCommitsNoRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	mine := filepath.Join(t.TempDir(), "mine")
	testGit(t, "", "init", "-q", "-b", "main", mine)
	commitFile(t, mine, "a.txt", "first")
	if _, err := pullCommits(context.Background(), mine, "work", localBundle(t, t.TempDir())); err == nil {
		t.Error("want an error")
	}
}
