package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func sandboxPullCmd(g *globals, f *sandboxFlags) *cobra.Command {
	var dir string
	var noMerge bool
	c := &cobra.Command{
		Use:   "pull [SESSION]",
		Short: "Bring a session's commits into this git repository",
		Long: `Fetch the commits a session has made into the git repository you're in.
Each branch of the session's repository arrives as sandbox/SESSION/BRANCH,
and the branch you're on moves forward to the session's branch of the same
name when it can (a fast-forward; --no-merge leaves it alone). From there
it's your own git: push, open a pull request.

Only commits come over: what the session hasn't committed stays there. The
session needs none of your git credentials for this, and gets none.

The session's repository is the directory it was started --from, or --dir.
SESSION can be left out when you have one session.`,
		Example: `  mcloud sandbox pull
  mcloud sandbox pull refactor --no-merge
  mcloud sandbox pull refactor --dir my-repo`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, err := gitIn(ctx, "", "rev-parse", "--show-toplevel")
			if err != nil {
				return errors.New("this isn't a git repository: run mcloud sandbox pull in the one the session's commits belong in")
			}
			s, err := g.existingSandbox(ctx, f)
			if err != nil {
				return err
			}
			session := ""
			if len(args) == 1 {
				session = args[0]
			}
			in, err := s.session(ctx, session)
			if err != nil {
				return err
			}
			from := sessionDir(in, dir)
			if from == "" {
				return fmt.Errorf("which directory in %s is the repository? Say with --dir (a session started --from a directory knows)", in.Name)
			}
			u := g.UI()
			step := u.Step("Fetching commits from " + in.Name)
			branches, err := pullCommits(ctx, repo, in.Name, func(ctx context.Context, have []string, w io.Writer) error {
				return s.k.SessionBundle(ctx, s.user, in.Name, from, have, w)
			})
			if err != nil {
				step.Fail("")
				return err
			}
			if len(branches) == 0 {
				step.Done("Nothing new in %s", in.Name)
			} else {
				step.Done("Fetched from %s", in.Name)
			}
			current, _ := gitIn(ctx, repo, "symbolic-ref", "--short", "-q", "HEAD")
			for _, b := range branches {
				u.Printf("  %s  %s", b.Ref, u.Dim(commits(b.New)))
				if b.Name != current || noMerge {
					if _, err := gitIn(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+b.Name); err == nil {
						u.Hint("git switch %s && git merge %s", b.Name, b.Ref)
					} else {
						u.Hint("git switch -c %s %s", b.Name, b.Ref)
					}
					continue
				}
				if _, err := gitIn(ctx, repo, "merge-base", "--is-ancestor", "HEAD", b.Commit); err != nil {
					u.Note("%s has commits the session's %s doesn't, so it stays where it is: git merge %s", current, b.Name, b.Ref)
					continue
				}
				if _, err := gitIn(ctx, repo, "merge", "--ff-only", "--quiet", b.Ref); err != nil {
					u.Warn("%s stays where it is: %v", current, err)
					u.Hint("git merge %s", b.Ref)
					continue
				}
				u.Success("%s now has them", current)
			}
			if dirty, err := s.k.SessionUncommitted(ctx, s.user, in.Name, from); err == nil && dirty {
				u.Note("%s has changes that aren't committed; they stay there. Have them committed (mcloud sandbox prompt %s 'Commit your work' --continue), or copy files out with mcloud sandbox cp.", in.Name, in.Name)
			}
			return nil
		},
	}
	dirFlag(c, &dir)
	c.Flags().BoolVar(&noMerge, "no-merge", false, "only fetch: leave the branch you're on where it is")
	return c
}

func commits(n int) string {
	if n == 1 {
		return "1 new commit"
	}
	return fmt.Sprintf("%d new commits", n)
}

// gitIn runs git in dir ("": here) and returns what it printed, trimmed.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], strings.Join(strings.Fields(msg), " "))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// pulledBranch is a branch of a session's repository, fetched.
type pulledBranch struct {
	// Name is the branch in the session; Ref is what it's called here.
	Name, Ref string
	Commit    string
	// New is how many of its commits no branch here has.
	New int
}

// maxHave is how many of this repository's commits the session is told
// about: its newest branch tips. More would only save copying.
const maxHave = 200

// pullCommits fetches the branches of a session's repository into repo as
// refs/remotes/sandbox/<session>/*, and returns the ones that brought
// something. bundle writes a git bundle of the session's branches, without
// what the commits it's given already hold.
//
// What comes from a session isn't trusted. It only ever becomes objects
// and remote-tracking refs, through git fetch with its checks on: nothing
// is checked out, no hook runs, no tag or branch of the user's is touched.
func pullCommits(ctx context.Context, repo, session string, bundle func(ctx context.Context, have []string, w io.Writer) error) ([]pulledBranch, error) {
	tips, err := gitIn(ctx, repo, "for-each-ref", "--sort=-committerdate", "--count="+strconv.Itoa(maxHave), "--format=%(objectname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var have []string
	seen := map[string]bool{}
	for _, tip := range strings.Fields(tips) {
		if !seen[tip] {
			seen[tip] = true
			have = append(have, tip)
		}
	}
	tmp, err := os.CreateTemp("", "mcloud-pull-*.bundle")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	err = bundle(ctx, have, tmp)
	info, statErr := tmp.Stat()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = statErr
	}
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, nil
	}
	heads, err := gitIn(ctx, repo, "bundle", "list-heads", tmp.Name())
	if err != nil {
		return nil, fmt.Errorf("what the session sent isn't a git bundle: %w", err)
	}
	prefix := "refs/remotes/sandbox/" + session + "/"
	if _, err := gitIn(ctx, repo, "-c", "fetch.fsckObjects=true", "fetch", "--quiet", "--no-tags", tmp.Name(), "+refs/heads/*:"+prefix+"*"); err != nil {
		return nil, err
	}
	var out []pulledBranch
	for _, line := range strings.Split(heads, "\n") {
		commit, ref, ok := strings.Cut(strings.TrimSpace(line), " ")
		name, isBranch := strings.CutPrefix(ref, "refs/heads/")
		if !ok || !isBranch {
			continue
		}
		b := pulledBranch{Name: name, Ref: "sandbox/" + session + "/" + name, Commit: commit}
		if n, err := gitIn(ctx, repo, "rev-list", "--count", commit, "--not", "--branches"); err == nil {
			b.New, _ = strconv.Atoi(n)
		}
		out = append(out, b)
	}
	return out, nil
}
