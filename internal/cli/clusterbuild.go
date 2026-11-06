package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

// buildWhere picks where a direct deploy builds: "local" (docker, pack)
// or "cluster" (a buildpacks pod in the app's namespace). mode is the
// --build flag: auto, local or cluster.
func buildWhere(ctx context.Context, dir, mode string) (string, error) {
	_, err := os.Stat(filepath.Join(dir, "Dockerfile"))
	dockerfile := err == nil
	_, dockerErr := exec.LookPath("docker")
	switch mode {
	case "local":
		return "local", nil
	case "cluster":
		if dockerfile {
			return "", errors.New("--build cluster builds with buildpacks; an app with a Dockerfile builds with docker (here, or in its GitOps workflow)")
		}
		return "cluster", nil
	case "", "auto":
		if dockerErr == nil {
			return "local", nil
		}
		if dockerfile {
			return "", errors.New("building with the Dockerfile needs docker, which isn't installed")
		}
		if packWorks(ctx) {
			return "", errors.New("pack builds with docker, which isn't installed; --build cluster builds in the cluster instead")
		}
		return "cluster", nil
	default:
		return "", fmt.Errorf("--build %q: auto, local or cluster", mode)
	}
}

// clusterBuildPush builds the app in its namespace and asks the image for
// its functions there too. Returns repo@digest.
func (g *globals) clusterBuildPush(ctx context.Context, k *kube.Client, p *project) (string, *functions.Description, error) {
	registry := imageRegistry()
	if p.Image != "" && hostOf(p.Image) != registry {
		return "", nil, fmt.Errorf("a cluster build pushes to the maxcloud registry, not %s (image: in maxcloud.yaml)", p.Image)
	}
	repo := registry + "/" + p.App + "/" + p.App
	token, _, err := k.RegistryPushToken(ctx, p.App, registry, time.Hour)
	if err != nil {
		return "", nil, err
	}
	env, err := projectBuildEnv(filepath.Join(p.Dir, "project.toml"))
	if err != nil {
		return "", nil, err
	}
	src, err := sourceTar(p.Dir)
	if err != nil {
		return "", nil, err
	}
	ref := repo + ":direct-" + time.Now().UTC().Format("20060102-150405")
	fmt.Fprintf(g.out, "Building %s in the cluster ...\n", ref)
	digest, err := k.Build(ctx, p.App, kube.ClusterBuild{
		Builder:  "paketobuildpacks/ubuntu-noble-builder:" + paketoBuilderVersion,
		Image:    ref,
		Cache:    repo + ":build-cache",
		Registry: registry,
		Token:    token,
		Env:      env,
	}, bytes.NewReader(src), g.errOut)
	if err != nil {
		return "", nil, err
	}
	image := repo + "@" + digest
	fmt.Fprintln(g.out, "Asking the image for its functions ...")
	out, err := k.Describe(ctx, p.App, image)
	if err != nil {
		return "", nil, err
	}
	d, err := functions.Parse(out)
	return image, d, err
}

// sourceTar is the app's files as a tar: what git tracks or would (not
// what .gitignore leaves out, like node_modules), or everything but .git
// and node_modules outside a git repo.
func sourceTar(dir string) ([]byte, error) {
	var files []string
	cmd := exec.Command("git", "ls-files", "-co", "--exclude-standard", "-z")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
	} else {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				rel, _ := filepath.Rel(dir, path)
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, rel := range files {
		path := filepath.Join(dir, rel)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted but still in git's index
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return nil, err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil, err
		}
		h.Name = filepath.ToSlash(rel)
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// projectBuildEnv reads the build-time env from project.toml's
// [[io.buildpacks.build.env]] tables (name and value), which pack applies
// itself and a bare lifecycle doesn't.
func projectBuildEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	in, name, value := false, "", ""
	flush := func() {
		if in && name != "" {
			env[name] = value
		}
		name, value = "", ""
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			flush()
			in = line == "[[io.buildpacks.build.env]]"
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		} else {
			v = strings.Trim(v, `'`)
		}
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "value":
			value = v
		}
	}
	flush()
	return env, sc.Err()
}
