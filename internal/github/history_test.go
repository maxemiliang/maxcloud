package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// gitFake is a branch of commits whose trees hold blobs by SHA: what
// RestoreDir and Commits read and write.
type gitFake struct {
	mu      sync.Mutex
	commits map[string]gitCommit
	order   []string // oldest first
	head    string
	n       int
	posted  []map[string]any
}

type gitCommit struct {
	files   map[string]string // path -> blob SHA ("b:" + content)
	message string
}

func (f *gitFake) commit(files map[string]string, message string) string {
	f.n++
	sha := fmt.Sprintf("%040d", f.n)
	f.commits[sha] = gitCommit{files: files, message: message}
	f.order = append(f.order, sha)
	f.head = sha
	return sha
}

func (f *gitFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/repos/o/r")
	switch {
	case r.Method == "GET" && p == "/git/ref/heads/main":
		json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": f.head}})
	case r.Method == "GET" && strings.HasPrefix(p, "/git/commits/"):
		json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "t" + strings.TrimPrefix(p, "/git/commits/")}})
	case r.Method == "GET" && strings.HasPrefix(p, "/git/trees/t"):
		c := f.commits[strings.TrimPrefix(p, "/git/trees/t")]
		var tree []map[string]string
		for path, sha := range c.files {
			tree = append(tree, map[string]string{"path": path, "mode": "100644", "type": "blob", "sha": sha})
		}
		json.NewEncoder(w).Encode(map[string]any{"tree": tree})
	case r.Method == "POST" && p == "/git/trees":
		var body struct {
			BaseTree string           `json:"base_tree"`
			Tree     []map[string]any `json:"tree"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		files := map[string]string{}
		for k, v := range f.commits[strings.TrimPrefix(body.BaseTree, "t")].files {
			files[k] = v
		}
		for _, e := range body.Tree {
			f.posted = append(f.posted, e)
			if sha, _ := e["sha"].(string); sha != "" {
				files[e["path"].(string)] = sha
			} else {
				delete(files, e["path"].(string))
			}
		}
		// The new tree is a commit-to-be; its SHA names it.
		f.n++
		id := fmt.Sprintf("%040d", f.n)
		f.commits[id] = gitCommit{files: files}
		json.NewEncoder(w).Encode(map[string]string{"sha": "t" + id})
	case r.Method == "POST" && p == "/git/commits":
		var body struct {
			Message string   `json:"message"`
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		id := strings.TrimPrefix(body.Tree, "t")
		c := f.commits[id]
		c.message = body.Message
		f.commits[id] = c
		json.NewEncoder(w).Encode(map[string]string{"sha": id})
	case r.Method == "PATCH" && p == "/git/refs/heads/main":
		var body struct {
			SHA string `json:"sha"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.head = body.SHA
		f.order = append(f.order, body.SHA)
		w.Write([]byte("{}"))
	case r.Method == "GET" && p == "/commits":
		// Newest first, those that changed path, from sha back.
		q := r.URL.Query()
		var out []map[string]any
		start := len(f.order) - 1
		for i, s := range f.order {
			if s == q.Get("sha") {
				start = i
			}
		}
		for i := start; i >= 0; i-- {
			s := f.order[i]
			changed := i == 0
			if i > 0 {
				changed = dirOf(f.commits[s].files, q.Get("path")) != dirOf(f.commits[f.order[i-1]].files, q.Get("path"))
			}
			if changed {
				out = append(out, map[string]any{"sha": s, "commit": map[string]any{"message": f.commits[s].message, "author": map[string]string{"name": "dev"}}, "author": map[string]string{"login": "dev-login"}})
			}
		}
		json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "unexpected "+r.Method+" "+p, 500)
	}
}

func dirOf(files map[string]string, dir string) string {
	var lines []string
	for p, s := range files {
		if strings.HasPrefix(p, dir+"/") {
			lines = append(lines, p+"="+s)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, ",")
}

func TestRestoreDirAndCommits(t *testing.T) {
	f := &gitFake{commits: map[string]gitCommit{}}
	f.commit(map[string]string{"main.go": "b:v1", "deploy/k8s/kustomization.yaml": "b:img1", "deploy/k8s/web.yaml": "b:web1"}, "deploy: aaa")
	old := f.head
	f.commit(map[string]string{"main.go": "b:v2", "deploy/k8s/kustomization.yaml": "b:img1", "deploy/k8s/web.yaml": "b:web1"}, "code")
	f.commit(map[string]string{"main.go": "b:v2", "deploy/k8s/kustomization.yaml": "b:img2", "deploy/k8s/web.yaml": "b:web2", "deploy/k8s/nightly.yaml": "b:cron"}, "deploy: bbb")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("token")
	c.BaseURL = srv.URL
	ctx := context.Background()

	cs, err := c.Commits(ctx, "o/r", "main", "deploy/k8s", 10)
	if err != nil || len(cs) != 2 || cs[0].Subject() != "deploy: bbb" || cs[1].SHA != old || cs[0].Author != "dev-login" {
		t.Fatalf("commits %+v, %v", cs, err)
	}

	sha, err := c.RestoreDir(ctx, "o/r", "main", "deploy/k8s", old, "rollback: to aaa [skip ci]")
	if err != nil || sha == "" {
		t.Fatalf("restore: %q, %v", sha, err)
	}
	got := f.commits[f.head]
	if f.head != sha || got.message != "rollback: to aaa [skip ci]" {
		t.Errorf("head %s, message %q", f.head, got.message)
	}
	want := map[string]string{"main.go": "b:v2", "deploy/k8s/kustomization.yaml": "b:img1", "deploy/k8s/web.yaml": "b:web1"}
	if fmt.Sprint(got.files) != fmt.Sprint(want) {
		t.Errorf("files after the rollback: %v", got.files)
	}
	// Again: nothing to do.
	if sha, err := c.RestoreDir(ctx, "o/r", "main", "deploy/k8s", old, "again"); err != nil || sha != "" {
		t.Errorf("second restore: %q, %v", sha, err)
	}
	// Commits from a SHA: what was deployed at that point.
	cs, err = c.Commits(ctx, "o/r", old, "deploy/k8s", 1)
	if err != nil || len(cs) != 1 || cs[0].SHA != old {
		t.Errorf("commits from %s: %+v, %v", old, cs, err)
	}
}
