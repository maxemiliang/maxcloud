package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sourceRepo is a repo whose root is a Go module and whose app is in
// worker/, with maxcloud.yaml saying source: worker.
func sourceRepo(t *testing.T, yaml string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "worker", "src"), 0o755)
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module cli\n"), 0o644)
	os.WriteFile(filepath.Join(root, "worker", "package.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(root, "worker", "src", "index.ts"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, ProjectFile), []byte(yaml), 0o644)
	return root
}

func TestSourceDir(t *testing.T) {
	root := sourceRepo(t, "app: shop\n")
	os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644)
	os.Symlink(t.TempDir(), filepath.Join(root, "out"))
	os.Symlink("worker", filepath.Join(root, "in"))
	for _, c := range []struct {
		source, want, err string
	}{
		{"", root, ""},
		{".", root, ""},
		{"worker", filepath.Join(root, "worker"), ""},
		{"./worker/", filepath.Join(root, "worker"), ""},
		{"worker/src", filepath.Join(root, "worker", "src"), ""},
		{"in", filepath.Join(root, "in"), ""},
		{"/etc", "", "relative"},
		{"../x", "", ".."},
		{"worker/../worker", "", ".."},
		{"nope", "", "isn't a directory"},
		{"file", "", "is a file"},
		{"out", "", "out of the repo"},
	} {
		p := &project{App: "shop", Dir: root, Source: c.source}
		got, err := p.sourceDir()
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("source %q: got %q, %v; want an error with %q", c.source, got, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("source %q: got %q, %v; want %q", c.source, got, err, c.want)
		}
	}
}

func TestLoadProjectSource(t *testing.T) {
	root := sourceRepo(t, "app: shop\nsource: worker\n")
	t.Chdir(filepath.Join(root, "worker", "src"))
	p, err := loadProject()
	if err != nil {
		t.Fatal(err)
	}
	if dir, err := p.sourceDir(); err != nil || dir != filepath.Join(p.Dir, "worker") {
		t.Errorf("got %q, %v", dir, err)
	}
}

func TestCIContext(t *testing.T) {
	root := sourceRepo(t, "app: shop\nsource: worker\n")
	t.Chdir(root)
	if got, err := ciContext(""); err != nil || got != "worker" {
		t.Errorf("source: got %q, %v", got, err)
	}
	if got, err := ciContext("other"); err != nil || got != "other" {
		t.Errorf("--context should win: got %q, %v", got, err)
	}
	os.WriteFile(ProjectFile, []byte("app: shop\nsource: ../up\n"), 0o644)
	if _, err := ciContext(""); err == nil {
		t.Error("want an error for source: ../up")
	}
	t.Chdir(t.TempDir())
	if got, err := ciContext(""); err != nil || got != "." {
		t.Errorf("no maxcloud.yaml: got %q, %v", got, err)
	}

	// mcloud build --print-context, which the deploy workflow runs.
	t.Chdir(root)
	os.WriteFile(ProjectFile, []byte("app: shop\nsource: worker\n"), 0o644)
	var out strings.Builder
	g := &globals{in: strings.NewReader(""), out: &out, errOut: io.Discard}
	cmd := buildCmd(g)
	cmd.SetArgs([]string{"--print-context", "--context", ""})
	if err := cmd.ExecuteContext(context.Background()); err != nil || out.String() != "worker\n" {
		t.Errorf("--print-context: %q, %v", out.String(), err)
	}
}

func TestBuildWhereLooksInSource(t *testing.T) {
	root := sourceRepo(t, "app: shop\nsource: worker\n")
	os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	// The root's Dockerfile isn't the app's: worker/ builds with buildpacks.
	if where, err := buildWhere(context.Background(), filepath.Join(root, "worker"), "cluster"); err != nil || where != "cluster" {
		t.Errorf("worker: %q, %v", where, err)
	}
	if _, err := buildWhere(context.Background(), root, "cluster"); err == nil {
		t.Error("root has a Dockerfile: want an error for --build cluster")
	}
}

func TestDevCommandRunsInSource(t *testing.T) {
	root := sourceRepo(t, "app: shop\nsource: worker\n")
	worker := filepath.Join(root, "worker")
	sdk := filepath.Join(worker, "node_modules", "@max-cloud", "functions")
	os.MkdirAll(sdk, 0o755)
	os.WriteFile(filepath.Join(sdk, "package.json"), []byte(`{"version": "0.4.0"}`), 0o644)
	cmd, err := devCommand(context.Background(), worker, "", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != worker || cmd.Args[0] != "node" {
		t.Errorf("dir %q, args %q", cmd.Dir, cmd.Args)
	}
	// The root is a Go module, but that's not the app.
	if cmd, err := devCommand(context.Background(), root, "", 8080); err != nil || cmd.Args[0] != "go" {
		t.Errorf("root: %v %v", cmd, err)
	}
}

func TestSourceTarOfSubfolder(t *testing.T) {
	root := sourceRepo(t, "app: shop\nsource: worker\n")
	b, err := sourceTar(filepath.Join(root, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tarNames(t, b), ","); got != "package.json,src/index.ts" {
		t.Errorf("names %v", got)
	}
}
