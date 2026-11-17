package cli

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/maxemiliang/maxcloud/internal/storage"
)

// fakeS3 is enough of S3 for mcloud bucket: path-style ListObjectsV2 (with
// a delimiter), PutObject, GetObject and DeleteObjects, in memory.
type fakeS3 struct {
	*httptest.Server
	mu      sync.Mutex
	buckets map[string]map[string][]byte
	types   map[string]string // bucket/key -> Content-Type
}

func newFakeS3(t *testing.T, buckets ...string) *fakeS3 {
	f := &fakeS3{buckets: map[string]map[string][]byte{}, types: map[string]string{}}
	for _, b := range buckets {
		f.buckets[b] = map[string][]byte{}
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	objects, ok := f.buckets[bucket]
	fail := func(code int, s3code string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(code)
		fmt.Fprintf(w, `<Error><Code>%s</Code><Message>%s</Message></Error>`, s3code, s3code)
	}
	if !ok {
		fail(http.StatusNotFound, "NoSuchBucket")
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && key == "" && q.Get("list-type") == "2":
		prefix, delim := q.Get("prefix"), q.Get("delimiter")
		var keys []string
		for k := range objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(`<ListBucketResult><IsTruncated>false</IsTruncated>`)
		seen := map[string]bool{}
		for _, k := range keys {
			if delim != "" {
				if i := strings.Index(k[len(prefix):], delim); i >= 0 {
					p := k[:len(prefix)+i+1]
					if !seen[p] {
						seen[p] = true
						fmt.Fprintf(&b, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, p)
					}
					continue
				}
			}
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-11-17T10:00:00Z</LastModified></Contents>`, k, len(objects[k]))
		}
		b.WriteString(`</ListBucketResult>`)
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, b.String())
	case r.Method == http.MethodPost && q.Has("delete"):
		var req struct {
			Objects []struct{ Key string } `xml:"Object"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(http.StatusBadRequest, "MalformedXML")
			return
		}
		for _, o := range req.Objects {
			delete(objects, o.Key)
		}
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<DeleteResult></DeleteResult>`)
	case r.Method == http.MethodPut && key != "":
		body, _ := io.ReadAll(r.Body)
		objects[key] = body
		f.types[bucket+"/"+key] = r.Header.Get("Content-Type")
	case r.Method == http.MethodGet && key != "":
		body, ok := objects[key]
		if !ok {
			fail(http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Write(body)
	default:
		fail(http.StatusNotImplemented, "NotImplemented")
	}
}

func (f *fakeS3) keys(bucket string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.buckets[bucket] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bucket runs mcloud bucket for the app shop against the fake.
func (f *fakeS3) bucket(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	g := &globals{in: strings.NewReader(""), out: &out, errOut: &out, plain: true,
		testStorage: func(app string) (*storage.Client, error) {
			return storage.New(app, map[string]string{
				"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret", "AWS_ENDPOINT_URL_S3": f.URL, "AWS_REGION": "europe-1",
			})
		}}
	cmd := newRoot(g)
	cmd.SetArgs(append([]string{"bucket", "--app", "shop"}, args...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	return out.String(), err
}

func TestBucketCopyListRemove(t *testing.T) {
	f := newFakeS3(t, "mc-shop--uploads")
	dir := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	logo := write("logo.png", "png")
	write("site/index.html", "<html>")
	write("site/css/app.css", "body{}")

	// A file into the bucket's root keeps its name; a key of its own renames it.
	if _, err := f.bucket(t, "cp", logo, "uploads:"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bucket(t, "cp", logo, "uploads:img/brand.png"); err != nil {
		t.Fatal(err)
	}
	// A directory, under a prefix.
	if out, err := f.bucket(t, "cp", "-r", filepath.Join(dir, "site"), "uploads:www/"); err != nil || !strings.Contains(out, "2 files") {
		t.Fatalf("cp -r: %v\n%s", err, out)
	}
	want := []string{"img/brand.png", "logo.png", "www/css/app.css", "www/index.html"}
	if got := f.keys("mc-shop--uploads"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if ct := f.types["mc-shop--uploads/www/css/app.css"]; !strings.HasPrefix(ct, "text/css") {
		t.Errorf("app.css uploaded as %q, want text/css", ct)
	}
	if _, err := f.bucket(t, "cp", filepath.Join(dir, "site"), "uploads:"); err == nil || !strings.Contains(err.Error(), "-r") {
		t.Errorf("a directory without -r: %v", err)
	}

	// One level, then everything.
	out, err := f.bucket(t, "ls", "uploads")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"img/", "www/", "logo.png"} {
		if !strings.Contains(out, s) {
			t.Errorf("ls: no %s in\n%s", s, out)
		}
	}
	if strings.Contains(out, "index.html") {
		t.Errorf("ls without -r went below the first level:\n%s", out)
	}
	if out, _ = f.bucket(t, "ls", "-r", "uploads:www/"); !strings.Contains(out, "www/css/app.css") || strings.Contains(out, "logo.png") {
		t.Errorf("ls -r uploads:www/:\n%s", out)
	}

	// Out again: one object, to stdout, and a prefix into a directory.
	if out, err = f.bucket(t, "cp", "uploads:www/index.html", "-"); err != nil || out != "<html>" {
		t.Errorf("cp to stdout: %q, %v", out, err)
	}
	back := filepath.Join(t.TempDir(), "back")
	if _, err := f.bucket(t, "cp", "-r", "uploads:www", back); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(back, "css", "app.css")); err != nil || string(b) != "body{}" {
		t.Errorf("downloaded app.css: %q, %v", b, err)
	}
	if _, err := f.bucket(t, "cp", "uploads:img/brand.png", back); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(back, "brand.png")); err != nil {
		t.Errorf("an object copied into a directory: %v", err)
	}
	if _, err := f.bucket(t, "cp", "uploads:nope.txt", back); err == nil || !strings.Contains(err.Error(), "no object nope.txt") {
		t.Errorf("a missing object: %v", err)
	}

	// Remove: one object, then a folder (asked about: no terminal here, so
	// it needs -y), then the rest.
	if _, err := f.bucket(t, "rm", "uploads:logo.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bucket(t, "rm", "uploads:www/"); err == nil || !strings.Contains(err.Error(), "-r") {
		t.Errorf("a folder without -r: %v", err)
	}
	if _, err := f.bucket(t, "rm", "-r", "uploads:www/"); err == nil {
		t.Error("rm -r without -y and without a terminal removed things unasked")
	}
	if got := f.keys("mc-shop--uploads"); len(got) != 3 {
		t.Fatalf("after a refused rm -r: %v", got)
	}
	if out, err := f.bucket(t, "rm", "-r", "-y", "uploads:www/"); err != nil || !strings.Contains(out, "2 objects") {
		t.Fatalf("rm -r -y: %v\n%s", err, out)
	}
	if _, err := f.bucket(t, "rm", "-r", "-y", "uploads:"); err != nil {
		t.Fatal(err)
	}
	if got := f.keys("mc-shop--uploads"); len(got) != 0 {
		t.Errorf("after emptying: %v", got)
	}
}

func TestBucketErrors(t *testing.T) {
	f := newFakeS3(t, "mc-shop--uploads")
	if _, err := f.bucket(t, "ls", "nope"); err == nil || !strings.Contains(err.Error(), "no bucket nope") {
		t.Errorf("an unknown bucket: %v", err)
	}
	if _, err := f.bucket(t, "cp", "a.txt", "b.txt"); err == nil || !strings.Contains(err.Error(), "BUCKET:KEY") {
		t.Errorf("two local paths: %v", err)
	}
	if _, err := f.bucket(t, "cp", "uploads:a", "uploads:b"); err == nil || !strings.Contains(err.Error(), "local path") {
		t.Errorf("two bucket paths: %v", err)
	}
	if _, err := f.bucket(t, "rm", "a.txt"); err == nil {
		t.Error("rm of a local path: no error")
	}

	// An object whose key climbs out of the prefix doesn't climb out of
	// the directory it's copied into.
	f.buckets["mc-shop--uploads"]["evil/../../escape.txt"] = []byte("x")
	dst := filepath.Join(t.TempDir(), "in")
	if _, err := f.bucket(t, "cp", "-r", "uploads:evil/", dst); err == nil || !strings.Contains(err.Error(), "isn't a path below") {
		t.Errorf("a key with ..: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "escape.txt")); err == nil {
		t.Error("escape.txt was written outside the directory")
	}
}

func TestBucketNoCredentials(t *testing.T) {
	if _, err := storage.New("shop", map[string]string{}); err == nil || !strings.Contains(err.Error(), "no bucket credentials") {
		t.Errorf("no Secret: %v", err)
	}
}

func TestSizeString(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := sizeString(n); got != want {
			t.Errorf("sizeString(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestBucketLsJSON(t *testing.T) {
	f := newFakeS3(t, "mc-shop--uploads")
	f.buckets["mc-shop--uploads"]["a.txt"] = []byte("abc")
	f.buckets["mc-shop--uploads"]["img/b.png"] = []byte("x")
	out, err := f.bucket(t, "ls", "uploads", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"bucket": "uploads"`, `"key": "img/"`, `"folder": true`, `"key": "a.txt"`, `"sizeBytes": 3`, `"modified": "2026-11-17T10:00:00Z"`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in\n%s", want, out)
		}
	}
}
