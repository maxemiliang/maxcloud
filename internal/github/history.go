package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Commit is one commit, as a history lists it.
type Commit struct {
	SHA     string
	Message string
	Author  string // GitHub login, else the git author's name
	Date    time.Time
}

// Subject is the message's first line.
func (c Commit) Subject() string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return s
}

// Commits lists the newest commits reachable from ref (a branch or SHA)
// that change path, newest first, at most n (up to 100).
func (c *Client) Commits(ctx context.Context, repo Repo, ref, path string, n int) ([]Commit, error) {
	var out []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	q := url.Values{"sha": {ref}, "path": {path}, "per_page": {fmt.Sprint(min(max(n, 1), 100))}}
	if err := c.do(ctx, http.MethodGet, repo.path("/commits?%s", q.Encode()), nil, &out); err != nil {
		return nil, err
	}
	commits := make([]Commit, 0, len(out))
	for _, o := range out {
		cm := Commit{SHA: o.SHA, Message: o.Commit.Message, Author: o.Commit.Author.Name, Date: o.Commit.Committer.Date}
		if o.Author != nil && o.Author.Login != "" {
			cm.Author = o.Author.Login
		}
		commits = append(commits, cm)
	}
	return commits, nil
}

// Dir lists the names of the files in a directory at ref; ErrNotFound if
// there's no such directory.
func (c *Client) Dir(ctx context.Context, repo Repo, ref, dir string) ([]string, error) {
	var out []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := c.do(ctx, http.MethodGet, repo.path("/contents/%s?ref=%s", escapePath(dir), url.QueryEscape(ref)), nil, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, e := range out {
		if e.Type == "file" {
			names = append(names, e.Name)
		}
	}
	return names, nil
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// files lists the blobs under dir in a tree, by path.
func (c *Client) files(ctx context.Context, repo Repo, tree, dir string) (map[string]treeEntry, error) {
	var out struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := c.do(ctx, http.MethodGet, repo.path("/git/trees/%s?recursive=1", tree), nil, &out); err != nil {
		return nil, err
	}
	if out.Truncated {
		return nil, errors.New("the repo's tree is too large for GitHub to list in one go")
	}
	prefix := strings.Trim(dir, "/") + "/"
	files := map[string]treeEntry{}
	for _, e := range out.Tree {
		if e.Type == "blob" && strings.HasPrefix(e.Path, prefix) {
			files[e.Path] = e
		}
	}
	return files, nil
}

// RestoreDir commits dir on branch as it was at commit from: one new
// commit on top of the branch's head (no history is rewritten), with the
// files from then and without those added since. The rest of the repo
// stays as it is. Returns the new commit's SHA, or "" if dir is already
// like that.
func (c *Client) RestoreDir(ctx context.Context, repo Repo, branch, dir, from, message string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		sha, err := c.restoreOnce(ctx, repo, branch, dir, from, message)
		if errors.Is(err, errConflict) {
			continue
		}
		return sha, err
	}
	return "", fmt.Errorf("%s moved three times while committing; try again", branch)
}

func (c *Client) treeOf(ctx context.Context, repo Repo, commit string) (string, error) {
	var out struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	err := c.do(ctx, http.MethodGet, repo.path("/git/commits/%s", commit), nil, &out)
	return out.Tree.SHA, err
}

func (c *Client) restoreOnce(ctx context.Context, repo Repo, branch, dir, from, message string) (string, error) {
	head, err := c.Head(ctx, repo, branch)
	if err != nil {
		return "", err
	}
	headTree, err := c.treeOf(ctx, repo, head)
	if err != nil {
		return "", err
	}
	fromTree, err := c.treeOf(ctx, repo, from)
	if err != nil {
		return "", err
	}
	now, err := c.files(ctx, repo, headTree, dir)
	if err != nil {
		return "", err
	}
	then, err := c.files(ctx, repo, fromTree, dir)
	if err != nil {
		return "", err
	}
	if len(then) == 0 {
		return "", fmt.Errorf("%s has no %s at %s", repo, dir, from)
	}
	var entries []map[string]any
	for p, e := range then {
		if cur, ok := now[p]; ok && cur.SHA == e.SHA && cur.Mode == e.Mode {
			continue
		}
		entries = append(entries, map[string]any{"path": p, "mode": e.Mode, "type": "blob", "sha": e.SHA})
	}
	for p, e := range now {
		if _, ok := then[p]; !ok {
			entries = append(entries, map[string]any{"path": p, "mode": e.Mode, "type": "blob", "sha": nil})
		}
	}
	if len(entries) == 0 {
		return "", nil
	}
	var newTree struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, repo.path("/git/trees"), map[string]any{"base_tree": headTree, "tree": entries}, &newTree); err != nil {
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
