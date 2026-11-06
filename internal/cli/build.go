package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/functions"
)

// The Paketo builder images are built with when there's no Dockerfile, here
// and in CI (run-deploy.yaml runs mcloud build).
// renovate: datasource=docker depName=paketobuildpacks/ubuntu-noble-builder
const paketoBuilderVersion = "0.0.213"

// The pack image buildpacks run in when pack isn't installed.
// renovate: datasource=docker depName=buildpacksio/pack
const packImageVersion = "0.40.10"

// credHelper is the docker credential helper mcloud is when run as
// docker-credential-mcloud: docker and pack ask it for the maxcloud
// registry's login, and nothing is stored in ~/.docker.
const credHelper = "mcloud"

// CredentialHelper is the docker credential helper protocol
// (https://github.com/docker/docker-credential-helpers): get prints a
// login for the server on stdin. The password is $MCLOUD_REGISTRY_TOKEN
// (a push token mcloud got for this build), or in GitHub Actions a fresh
// OIDC token of the workflow, whose audience is the registry's host (those
// last about 5 minutes, less than a build, hence asking on every get).
func CredentialHelper(args []string, in io.Reader, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: docker-credential-mcloud get|store|erase|list")
		return 1
	}
	switch args[0] {
	case "get":
		b, err := io.ReadAll(in)
		if err != nil {
			fmt.Fprintln(out, err)
			return 1
		}
		server := strings.TrimSpace(string(b))
		token, err := registryToken(context.Background(), hostOf(server))
		if err != nil {
			// What docker takes as "no login": it then goes on anonymously.
			fmt.Fprintln(out, "credentials not found in native keychain")
			fmt.Fprintln(os.Stderr, "docker-credential-mcloud:", err)
			return 1
		}
		_ = json.NewEncoder(out).Encode(map[string]string{"ServerURL": server, "Username": "maxcloud", "Secret": token})
	case "list":
		fmt.Fprintln(out, "{}")
	default: // store, erase: nothing is stored
		_, _ = io.Copy(io.Discard, in)
	}
	return 0
}

func hostOf(server string) string {
	server = strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	return strings.SplitN(server, "/", 2)[0]
}

// registryToken is the password for the maxcloud registry: the push token
// in $MCLOUD_REGISTRY_TOKEN, else GitHub Actions' OIDC token.
func registryToken(ctx context.Context, host string) (string, error) {
	if t := os.Getenv("MCLOUD_REGISTRY_TOKEN"); t != "" {
		return t, nil
	}
	u, reqToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if u == "" || reqToken == "" {
		return "", errors.New("no registry token: not run by mcloud, and not in GitHub Actions with id-token: write")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"&audience="+host, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "bearer "+reqToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		Value string `json:"value"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&v) != nil || v.Value == "" {
		return "", fmt.Errorf("GitHub's OIDC token: %s", resp.Status)
	}
	return v.Value, nil
}

// dockerEnv runs docker and pack with a docker config of their own: yours,
// plus mcloud as the credential helper for the maxcloud registry. Nothing
// is written to ~/.docker.
type dockerEnv struct {
	dir      string
	registry string
	token    string // the push token; "" in GitHub Actions
	env      []string
}

func newDockerEnv(registry, token string) (*dockerEnv, error) {
	dir, err := os.MkdirTemp("", "mcloud-docker-")
	if err != nil {
		return nil, err
	}
	d := &dockerEnv{dir: dir, registry: registry, token: token}
	userDir := os.Getenv("DOCKER_CONFIG")
	if userDir == "" {
		home, _ := os.UserHomeDir()
		userDir = filepath.Join(home, ".docker")
	}
	cfg := map[string]any{}
	if b, err := os.ReadFile(filepath.Join(userDir, "config.json")); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	helpers, _ := cfg["credHelpers"].(map[string]any)
	if helpers == nil {
		helpers = map[string]any{}
	}
	helpers[registry] = credHelper
	cfg["credHelpers"] = helpers
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		d.close()
		return nil, err
	}
	// Builders, contexts and CLI plugins stay yours.
	if entries, err := os.ReadDir(userDir); err == nil {
		for _, e := range entries {
			if e.Name() != "config.json" {
				_ = os.Symlink(filepath.Join(userDir, e.Name()), filepath.Join(dir, e.Name()))
			}
		}
	}
	bin := filepath.Join(dir, "mcloud-bin")
	self, err := os.Executable()
	if err == nil {
		err = os.MkdirAll(bin, 0o700)
	}
	if err == nil {
		err = os.Symlink(self, filepath.Join(bin, "docker-credential-"+credHelper))
	}
	if err != nil {
		d.close()
		return nil, err
	}
	d.env = append(os.Environ(), "DOCKER_CONFIG="+dir, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if token != "" {
		d.env = append(d.env, "MCLOUD_REGISTRY_TOKEN="+token)
	}
	return d, nil
}

func (d *dockerEnv) close() { _ = os.RemoveAll(d.dir) }

// containerConfig is a docker config directory with the login written in,
// for pack running in a container (where mcloud isn't).
func (d *dockerEnv) containerConfig() (string, error) {
	dir := filepath.Join(d.dir, "container")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cfg := map[string]any{}
	if d.token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("maxcloud:" + d.token))
		cfg["auths"] = map[string]any{d.registry: map[string]string{"auth": auth}}
	}
	b, _ := json.Marshal(cfg)
	return dir, os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644)
}

func (d *dockerEnv) command(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = d.env
	return cmd
}

// buildSpec is one image build.
type buildSpec struct {
	dir   string // the build context
	ref   string // repo:tag to push
	cache string // repo:tag of the build cache ("" for none)
	ci    bool   // in CI: buildx has a container builder, so the cache can be exported
}

// buildImage builds the context into an image and pushes it: with its
// Dockerfile if it has one, else with buildpacks (pack, or pack in a
// container if pack isn't installed).
func (g *globals) buildImage(ctx context.Context, d *dockerEnv, s buildSpec) error {
	run := func(name string, args ...string) error {
		cmd := d.command(ctx, s.dir, name, args...)
		cmd.Stdout, cmd.Stderr = g.errOut, g.errOut
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %w", name, args[0], err)
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.dir, "Dockerfile")); err == nil {
		if _, err := exec.LookPath("docker"); err != nil {
			return errors.New("building with the Dockerfile needs docker")
		}
		// The cluster's nodes are amd64.
		args := []string{"buildx", "build", "--platform", "linux/amd64", "--push", "-t", s.ref}
		if s.ci && s.cache != "" {
			args = append(args, "--cache-from", "type=registry,ref="+s.cache,
				"--cache-to", "type=registry,ref="+s.cache+",mode=max,image-manifest=true,oci-mediatypes=true")
		}
		return run("docker", append(args, ".")...)
	}
	builder := "paketobuildpacks/ubuntu-noble-builder:" + paketoBuilderVersion
	packArgs := func(path string) []string {
		a := []string{"build", s.ref, "--path", path, "--builder", builder, "--publish"}
		if s.cache != "" {
			a = append(a, "--cache-image", s.cache)
		}
		if extra := os.Getenv("MCLOUD_PACK_FLAGS"); extra != "" {
			a = append(a, strings.Fields(extra)...)
		}
		return a
	}
	if packWorks(ctx) {
		return run("pack", packArgs(".")...)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("building without a Dockerfile needs pack or docker")
	}
	// pack in a container, talking to your docker daemon.
	cfg, err := d.containerConfig()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(s.dir)
	if err != nil {
		return err
	}
	sock := "/var/run/docker.sock"
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		sock = strings.TrimPrefix(h, "unix://")
	}
	fmt.Fprintln(g.errOut, "(pack isn't installed: running it in a container)")
	args := []string{"run", "--rm", "-v", sock + ":/var/run/docker.sock", "-v", abs + ":/workspace", "-v", cfg + ":/docker-config:ro",
		"-e", "DOCKER_CONFIG=/docker-config", "buildpacksio/pack:" + packImageVersion}
	return run("docker", append(args, packArgs("/workspace")...)...)
}

// packWorks: pack is installed and runs (a version manager's shim with no
// version set is on the PATH but doesn't).
func packWorks(ctx context.Context) bool {
	if _, err := exec.LookPath("pack"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "pack", "--version").Run() == nil
}

// imageDigest is the pushed image's digest.
func (g *globals) imageDigest(ctx context.Context, d *dockerEnv, ref string) (string, error) {
	out, err := cmdOutput(d.command(ctx, "", "docker", "buildx", "imagetools", "inspect", ref, "--format", "{{json .Manifest.Digest}}"))
	if err != nil {
		return "", fmt.Errorf("reading the pushed image's digest: %w", err)
	}
	return strings.Trim(out, `"`), nil
}

// describeImage asks a built functions image for its functions.
func (g *globals) describeImage(ctx context.Context, d *dockerEnv, image string) (*functions.Description, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := cmdOutput(d.command(ctx, "", "docker", "run", "--rm", "--pull", "missing", "-e", "MAXCLOUD_DESCRIBE=1", image))
	if err != nil {
		return nil, fmt.Errorf("describing the image: %w", err)
	}
	return functions.Parse([]byte(out))
}

func cmdOutput(cmd *exec.Cmd) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func buildCmd(g *globals) *cobra.Command {
	var o ciOptions
	c := &cobra.Command{
		Use:   "build",
		Short: "Build the app's image and push it to the maxcloud registry",
		Long: `Build the app in the current repo (with its Dockerfile, else with Paketo
buildpacks) and push it to the maxcloud registry,
registry.gronblom.dev/<app>/<app>. mcloud is docker's credential helper
for the registry while it builds, so there's no docker login.

--ci is the deploy workflow (run-deploy.yaml): it pushes as the repo (GitHub
Actions' OIDC token), writes the functions' manifests (--functions), pins
the image by digest in the manifests and commits that. Outside CI it
pushes with an hour-long token of the app's push ServiceAccount and prints
the image.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.ci {
				return g.ciBuild(cmd.Context(), o)
			}
			p, err := loadProject()
			if err != nil {
				return err
			}
			k, err := g.kube()
			if err != nil {
				return err
			}
			image, _, err := g.buildPush(cmd.Context(), k, p, false)
			if err != nil {
				return err
			}
			fmt.Fprintln(g.out, image)
			return nil
		},
	}
	f := c.Flags()
	f.BoolVar(&o.ci, "ci", false, "the deploy workflow's build (GitHub Actions): build, push, generate, pin and commit")
	f.StringVar(&o.context, "context", ".", "directory to build (--ci)")
	f.StringVar(&o.manifests, "manifests", "deploy/k8s", "the manifests directory Flux applies (--ci)")
	f.StringVar(&o.image, "image", "app", "the image's name in the manifests, replaced by the built one (--ci)")
	f.BoolVar(&o.functions, "functions", false, "a maxcloud functions app: write its functions' manifests (--ci)")
	f.StringVar(&o.app, "app", "", "the app (default: maxcloud.yaml's)")
	return c
}
