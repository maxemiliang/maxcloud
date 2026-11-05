package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"sigs.k8s.io/yaml"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kustomize"
)

// secretsFile is where a GitOps app keeps its secrets: in its manifests
// directory, SOPS-encrypted to the homelab's age key, which Flux decrypts
// when it applies the app.
const secretsFile = "secrets.sops.yaml"

var secretNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// reservedSecret: names the platform sets itself, which a secret would
// shadow or be shadowed by.
func reservedSecret(name string) bool {
	return name == "DATABASE_URL" || name == "FUNCTION_TARGET" || name == "PORT" ||
		strings.HasPrefix(name, "AWS_") || strings.HasPrefix(name, "MAXCLOUD_") || strings.HasPrefix(name, "K_")
}

func secretsCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "secrets",
		Short: "An app's secrets: environment variables for all its functions",
		Long: `Secrets reach every function of the app as environment variables (the
SDKs' secret("NAME") reads one). Run these in the app's repo.

GitOps apps keep them in deploy/k8s/` + secretsFile + `, encrypted with SOPS to
the homelab's age key (from the registry repo's .sops.yaml, or
$MCLOUD_AGE_RECIPIENT); commit and push it, or mcloud deploy. Changing one
needs the sops command, and your age key to decrypt the others.

Direct apps keep them in the Secret ` + functions.AppSecrets + ` on the cluster.

New values reach functions as they start again (the next deploy, or a
scale from zero).`,
	}
	c.AddCommand(secretsSetCmd(g), secretsUnsetCmd(g), secretsListCmd(g))
	return c
}

type secretsFlags struct{ dir string }

func (f *secretsFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.dir, "dir", "deploy/k8s", "GitOps apps: the manifests directory, relative to the repo")
}

func secretsSetCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "set NAME",
		Short: "Set a secret; the value comes from stdin (or a hidden prompt)",
		Example: `  mcloud secrets set STRIPE_KEY            # asks for the value
  op read op://vault/stripe/key | mcloud secrets set STRIPE_KEY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !secretNameRe.MatchString(name) {
				return fmt.Errorf("secret %q: use an environment variable name: A-Z, 0-9 and _, not starting with a digit", name)
			}
			if reservedSecret(name) {
				return fmt.Errorf("secret %q: the platform sets that one itself", name)
			}
			value, err := readSecretValue(g, name)
			if err != nil {
				return err
			}
			return g.editSecrets(cmd.Context(), f, func(s map[string]string) error {
				s[name] = value
				return nil
			}, "set "+name)
		},
	}
	f.bind(c)
	return c
}

func secretsUnsetCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "unset NAME",
		Short: "Remove a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			return g.editSecrets(cmd.Context(), f, func(s map[string]string) error {
				if _, ok := s[name]; !ok {
					return fmt.Errorf("no secret %s", name)
				}
				delete(s, name)
				return nil
			}, "removed "+name)
		},
	}
	f.bind(c)
	return c
}

func secretsListCmd(g *globals) *cobra.Command {
	var f secretsFlags
	c := &cobra.Command{
		Use:   "list",
		Short: "List the app's secrets (names only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject()
			if err != nil {
				return err
			}
			var names []string
			if p.Deploy == "direct" {
				k, err := g.kube()
				if err != nil {
					return err
				}
				data, err := k.SecretData(cmd.Context(), p.App, functions.AppSecrets)
				if err != nil {
					return err
				}
				for n := range data {
					names = append(names, n)
				}
			} else {
				// SOPS leaves the keys readable; no key needed to list them.
				raw, err := os.ReadFile(filepath.Join(p.Dir, f.dir, secretsFile))
				if errors.Is(err, os.ErrNotExist) {
					raw = nil
				} else if err != nil {
					return err
				}
				if names, err = sopsKeys(raw); err != nil {
					return err
				}
			}
			sort.Strings(names)
			if len(names) == 0 {
				fmt.Fprintln(g.out, "(no secrets)")
			}
			for _, n := range names {
				fmt.Fprintln(g.out, n)
			}
			return nil
		},
	}
	f.bind(c)
	return c
}

// editSecrets reads the app's secrets, changes them with edit, and writes
// them back where they live.
func (g *globals) editSecrets(ctx context.Context, f secretsFlags, edit func(map[string]string) error, done string) error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if p.App == "" {
		return errors.New("maxcloud.yaml has no app")
	}

	if p.Deploy == "direct" {
		k, err := g.kube()
		if err != nil {
			return err
		}
		s, err := k.SecretData(ctx, p.App, functions.AppSecrets)
		if err != nil {
			return err
		}
		if err := edit(s); err != nil {
			return err
		}
		if err := k.ApplySecret(ctx, p.App, functions.AppSecrets, s); err != nil {
			return err
		}
		fmt.Fprintf(g.out, "%s in Secret %s/%s. Functions get it when they next start.\n", done, p.App, functions.AppSecrets)
		return nil
	}

	dir := filepath.Join(p.Dir, f.dir)
	file := filepath.Join(dir, secretsFile)
	s := map[string]string{}
	if _, err := os.Stat(file); err == nil {
		plain, err := runSops(ctx, nil, "decrypt", "--input-type", "yaml", "--output-type", "yaml", file)
		if err != nil {
			return fmt.Errorf("decrypt %s (needs your age key, as for any SOPS file): %w", file, err)
		}
		if s, err = secretStringData(plain); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	if err := edit(s); err != nil {
		return err
	}
	recipient, err := g.ageRecipient(ctx)
	if err != nil {
		return err
	}
	enc, err := runSops(ctx, secretManifest(s), "encrypt",
		"--age", recipient,
		"--encrypted-regex", "^(data|stringData)$",
		"--input-type", "yaml", "--output-type", "yaml",
		"--filename-override", file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(file, enc, 0o644); err != nil {
		return err
	}
	if err := addToKustomization(dir, secretsFile); err != nil {
		return err
	}
	rel, _ := filepath.Rel(p.Dir, file)
	fmt.Fprintf(g.out, "%s in %s (encrypted). Commit and push it, or mcloud deploy.\n", done, rel)
	return nil
}

// ageRecipient is the homelab's age public key: $MCLOUD_AGE_RECIPIENT, or
// the first age recipient in the registry repo's .sops.yaml.
func (g *globals) ageRecipient(ctx context.Context) (string, error) {
	if r := os.Getenv("MCLOUD_AGE_RECIPIENT"); r != "" {
		return r, nil
	}
	gh, err := g.github()
	if err != nil {
		return "", err
	}
	data, err := gh.File(ctx, github.Repo(g.registry), g.branch, ".sops.yaml")
	if err != nil {
		return "", fmt.Errorf("the registry's .sops.yaml (for the age key to encrypt to; or set MCLOUD_AGE_RECIPIENT): %w", err)
	}
	r := ageRecipientRe.FindStringSubmatch(string(data))
	if r == nil {
		return "", fmt.Errorf("no age recipient in %s's .sops.yaml; set MCLOUD_AGE_RECIPIENT", g.registry)
	}
	return r[1], nil
}

var ageRecipientRe = regexp.MustCompile(`(?m)^\s*age:\s*['"]?(age1[0-9a-z]+)`)

// secretManifest is the Secret holding the app's secrets, as YAML.
func secretManifest(s map[string]string) []byte {
	m := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": functions.AppSecrets},
		"type":       "Opaque",
		"stringData": s,
	}
	out, _ := yaml.Marshal(m)
	return out
}

// secretStringData reads a decrypted Secret's values.
func secretStringData(plain []byte) (map[string]string, error) {
	var sec struct {
		StringData map[string]string `json:"stringData"`
		Data       map[string][]byte `json:"data"`
	}
	if err := yaml.Unmarshal(plain, &sec); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range sec.Data {
		out[k] = string(v)
	}
	for k, v := range sec.StringData {
		out[k] = v
	}
	return out, nil
}

// sopsKeys lists the value names of an encrypted Secret (SOPS encrypts
// values, not keys).
func sopsKeys(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var sec struct {
		StringData map[string]any `json:"stringData"`
		Data       map[string]any `json:"data"`
	}
	if err := yaml.Unmarshal(raw, &sec); err != nil {
		return nil, err
	}
	var names []string
	for k := range sec.StringData {
		names = append(names, k)
	}
	for k := range sec.Data {
		names = append(names, k)
	}
	return names, nil
}

func addToKustomization(dir, res string) error {
	file := filepath.Join(dir, "kustomization.yaml")
	k, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no %s: is %s the app's manifests directory (--dir)?", file, dir)
	}
	if err != nil {
		return err
	}
	ks := strings.Replace(string(k), "resources: []\n", "resources:\n", 1)
	ks, changed, err := kustomize.AddResource(ks, res)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if !changed {
		return nil
	}
	return os.WriteFile(file, []byte(ks), 0o644)
}

func runSops(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("sops"); err != nil {
		return nil, errors.New("needs the sops command (e.g. mise use -g sops)")
	}
	cmd := exec.CommandContext(ctx, "sops", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// readSecretValue reads the value from stdin: a hidden prompt on a
// terminal, else all of it (one trailing newline dropped).
func readSecretValue(g *globals, name string) (string, error) {
	if f, ok := g.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(g.errOut, "Value for %s: ", name)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(g.errOut)
		if err != nil {
			return "", err
		}
		if len(b) == 0 {
			return "", errors.New("empty value")
		}
		return string(b), nil
	}
	b, err := io.ReadAll(g.in)
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", errors.New("empty value on stdin")
	}
	return v, nil
}
