// Package github is the little of GitHub's REST API that mcloud needs: read
// files, and commit several file changes at once to a branch.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNotFound is returned for a missing repo, branch or file.
var ErrNotFound = errors.New("not found")

// errConflict means the branch moved while committing.
var errConflict = errors.New("branch moved")

type Client struct {
	BaseURL string // https://api.github.com
	Token   string
	HTTP    *http.Client
}

// Token finds a GitHub token: $GH_TOKEN, $GITHUB_TOKEN, or the GitHub CLI's
// login (`gh auth token`).
func Token() (string, error) {
	for _, v := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := os.Getenv(v); t != "" {
			return t, nil
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t, nil
		}
	}
	return "", errors.New("no GitHub token: run `gh auth login`, or set GH_TOKEN")
}

func New(token string) *Client {
	return &Client{
		BaseURL: "https://api.github.com",
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "mcloud")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode == http.StatusUnprocessableEntity && strings.Contains(string(data), "fast forward"):
		return errConflict
	case resp.StatusCode >= 300:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("%s %s: %s (HTTP %d)", method, path, e.Message, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Repo is "<owner>/<name>".
type Repo string

func (r Repo) path(format string, a ...any) string {
	return "/repos/" + string(r) + fmt.Sprintf(format, a...)
}

// RepoExists reports whether the token can see the repo.
func (c *Client) RepoExists(ctx context.Context, repo Repo) (bool, error) {
	err := c.do(ctx, http.MethodGet, repo.path(""), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// File reads a file at ref (a branch name or commit SHA).
func (c *Client) File(ctx context.Context, repo Repo, ref, file string) ([]byte, error) {
	var out struct {
		Type     string `json:"type"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	err := c.do(ctx, http.MethodGet, repo.path("/contents/%s?ref=%s", escapePath(file), url.QueryEscape(ref)), nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Type != "file" || out.Encoding != "base64" {
		return nil, fmt.Errorf("%s is not a regular file", file)
	}
	return base64.StdEncoding.DecodeString(out.Content)
}

// Exists reports whether a file or directory exists at ref.
func (c *Client) Exists(ctx context.Context, repo Repo, ref, p string) (bool, error) {
	err := c.do(ctx, http.MethodGet, repo.path("/contents/%s?ref=%s", escapePath(p), url.QueryEscape(ref)), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func escapePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Change is one file in a commit. A nil Content deletes the file.
type Change struct {
	Path    string
	Content []byte
}

// Reader reads a file as of the commit being built on; ErrNotFound if absent.
type Reader func(path string) ([]byte, error)

// Commit builds one commit on branch from the changes plan returns, and moves
// the branch to it. If the branch moves in the meantime, it starts over from
// the new head, so plan must be safe to call more than once. A plan that
// returns no changes makes no commit. Returns the new commit's SHA.
func (c *Client) Commit(ctx context.Context, repo Repo, branch, message string, plan func(Reader) ([]Change, error)) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		sha, err := c.commitOnce(ctx, repo, branch, message, plan)
		if errors.Is(err, errConflict) {
			continue
		}
		return sha, err
	}
	return "", fmt.Errorf("%s moved three times while committing; try again", branch)
}

func (c *Client) commitOnce(ctx context.Context, repo Repo, branch, message string, plan func(Reader) ([]Change, error)) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, repo.path("/git/ref/heads/%s", branch), nil, &ref); err != nil {
		return "", err
	}
	head := ref.Object.SHA
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.do(ctx, http.MethodGet, repo.path("/git/commits/%s", head), nil, &commit); err != nil {
		return "", err
	}

	changes, err := plan(func(p string) ([]byte, error) { return c.File(ctx, repo, head, p) })
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}

	// Each entry has either content (write the file) or "sha": null (delete
	// it); GitHub rejects an entry with both.
	var entries []map[string]any
	for _, ch := range changes {
		e := map[string]any{"path": ch.Path, "mode": "100644", "type": "blob"}
		if ch.Content != nil {
			e["content"] = string(ch.Content)
		} else {
			e["sha"] = nil
		}
		entries = append(entries, e)
	}
	var newTree struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, repo.path("/git/trees"), map[string]any{"base_tree": commit.Tree.SHA, "tree": entries}, &newTree); err != nil {
		return "", err
	}

	var newCommit struct {
		SHA string `json:"sha"`
	}
	body := map[string]any{"message": message, "tree": newTree.SHA, "parents": []string{head}}
	if err := c.do(ctx, http.MethodPost, repo.path("/git/commits"), body, &newCommit); err != nil {
		return "", err
	}
	if err := c.do(ctx, http.MethodPatch, repo.path("/git/refs/heads/%s", branch), map[string]any{"sha": newCommit.SHA, "force": false}, nil); err != nil {
		return "", err
	}
	return newCommit.SHA, nil
}

// Run is a GitHub Actions workflow run.
type Run struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued, in_progress, completed, ...
	Conclusion string `json:"conclusion"` // success, failure, cancelled, ...
	HTMLURL    string `json:"html_url"`
	Event      string `json:"event"`
	Path       string `json:"path"` // the workflow file: .github/workflows/deploy.yaml
}

// RunsFor lists the workflow runs for a commit, newest first.
func (c *Client) RunsFor(ctx context.Context, repo Repo, sha string) ([]Run, error) {
	var out struct {
		WorkflowRuns []Run `json:"workflow_runs"`
	}
	err := c.do(ctx, http.MethodGet, repo.path("/actions/runs?head_sha=%s&per_page=20", url.QueryEscape(sha)), nil, &out)
	return out.WorkflowRuns, err
}

// Run reads one workflow run.
func (c *Client) Run(ctx context.Context, repo Repo, id int64) (*Run, error) {
	var r Run
	err := c.do(ctx, http.MethodGet, repo.path("/actions/runs/%d", id), nil, &r)
	return &r, err
}

// Dispatch starts a workflow that has a workflow_dispatch trigger.
func (c *Client) Dispatch(ctx context.Context, repo Repo, workflow, ref string) error {
	return c.do(ctx, http.MethodPost, repo.path("/actions/workflows/%s/dispatches", url.PathEscape(workflow)), map[string]any{"ref": ref}, nil)
}

// Head is the commit a branch points at.
func (c *Client) Head(ctx context.Context, repo Repo, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.do(ctx, http.MethodGet, repo.path("/git/ref/heads/%s", branch), nil, &ref)
	return ref.Object.SHA, err
}
