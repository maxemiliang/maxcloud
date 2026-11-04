package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub keeps a branch as a list of commits, each a full file map.
type fakeGitHub struct {
	mu      sync.Mutex
	commits map[string]map[string]string // sha -> files
	head    string
	trees   map[string]map[string]string
	n       int
	// moveOnce makes the first ref update fail as if someone pushed first.
	moveOnce bool
	patches  int
	posted   []map[string]any
}

func newFake(files map[string]string) *fakeGitHub {
	f := &fakeGitHub{commits: map[string]map[string]string{}, trees: map[string]map[string]string{}}
	f.head = f.add(files)
	return f
}

func (f *fakeGitHub) add(files map[string]string) string {
	f.n++
	sha := fmt.Sprintf("c%d", f.n)
	f.commits[sha] = files
	f.trees["t"+sha] = files
	return sha
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/repos/o/r")
	switch {
	case r.Method == "GET" && p == "/git/ref/heads/main":
		json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": f.head}})
	case r.Method == "GET" && strings.HasPrefix(p, "/git/commits/"):
		sha := strings.TrimPrefix(p, "/git/commits/")
		json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "t" + sha}})
	case r.Method == "GET" && strings.HasPrefix(p, "/contents/"):
		files := f.commits[r.URL.Query().Get("ref")]
		c, ok := files[strings.TrimPrefix(p, "/contents/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(c))})
	case r.Method == "POST" && p == "/git/trees":
		var body struct {
			BaseTree string           `json:"base_tree"`
			Tree     []map[string]any `json:"tree"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		files := map[string]string{}
		for k, v := range f.trees[body.BaseTree] {
			files[k] = v
		}
		for _, e := range body.Tree {
			f.posted = append(f.posted, e)
			path := e["path"].(string)
			if c, ok := e["content"]; ok {
				if _, both := e["sha"]; both {
					http.Error(w, `{"message":"content and sha"}`, 422)
					return
				}
				files[path] = c.(string)
			} else if sha, ok := e["sha"]; ok && sha == nil {
				delete(files, path)
			}
		}
		f.n++
		t := fmt.Sprintf("t%d", f.n)
		f.trees[t] = files
		json.NewEncoder(w).Encode(map[string]string{"sha": t})
	case r.Method == "POST" && p == "/git/commits":
		var body struct {
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.n++
		sha := fmt.Sprintf("c%d", f.n)
		f.commits[sha] = f.trees[body.Tree]
		f.trees["t"+sha] = f.trees[body.Tree]
		json.NewEncoder(w).Encode(map[string]string{"sha": sha})
	case r.Method == "PATCH" && p == "/git/refs/heads/main":
		f.patches++
		var body struct {
			SHA string `json:"sha"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if f.moveOnce {
			f.moveOnce = false
			files := map[string]string{}
			for k, v := range f.commits[f.head] {
				files[k] = v
			}
			files["other.txt"] = "pushed meanwhile"
			f.head = f.add(files)
			http.Error(w, `{"message":"Update is not a fast forward"}`, 422)
			return
		}
		f.head = body.SHA
		w.Write([]byte("{}"))
	default:
		http.Error(w, "unexpected "+r.Method+" "+p, 500)
	}
}

func client(t *testing.T, f *fakeGitHub) *Client {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("token")
	c.BaseURL = srv.URL
	return c
}

func TestCommitWritesAndDeletes(t *testing.T) {
	f := newFake(map[string]string{"k.yaml": "a\n", "old.yaml": "x"})
	c := client(t, f)
	sha, err := c.Commit(context.Background(), "o/r", "main", "msg", func(read Reader) ([]Change, error) {
		k, err := read("k.yaml")
		if err != nil {
			return nil, err
		}
		if _, err := read("new.yaml"); !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("want not found, got %v", err)
		}
		return []Change{
			{Path: "k.yaml", Content: append(k, "b\n"...)},
			{Path: "new.yaml", Content: []byte("new")},
			{Path: "old.yaml"},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.commits[f.head]
	if sha != f.head || got["k.yaml"] != "a\nb\n" || got["new.yaml"] != "new" {
		t.Errorf("head %s files %v", sha, got)
	}
	if _, ok := got["old.yaml"]; ok {
		t.Error("old.yaml not deleted")
	}
}

func TestCommitRetriesWhenBranchMoves(t *testing.T) {
	f := newFake(map[string]string{"k.yaml": "a\n"})
	f.moveOnce = true
	c := client(t, f)
	calls := 0
	_, err := c.Commit(context.Background(), "o/r", "main", "msg", func(read Reader) ([]Change, error) {
		calls++
		return []Change{{Path: "new.yaml", Content: []byte("new")}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.commits[f.head]
	if calls != 2 || f.patches != 2 || got["other.txt"] == "" || got["new.yaml"] != "new" {
		t.Errorf("calls %d patches %d files %v", calls, f.patches, got)
	}
}

func TestCommitNothingToDo(t *testing.T) {
	f := newFake(map[string]string{})
	c := client(t, f)
	sha, err := c.Commit(context.Background(), "o/r", "main", "msg", func(Reader) ([]Change, error) { return nil, nil })
	if err != nil || sha != "" || f.patches != 0 {
		t.Errorf("sha %q err %v patches %d", sha, err, f.patches)
	}
}
