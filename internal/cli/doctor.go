package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/maxemiliang/maxcloud/internal/auth"
	"github.com/maxemiliang/maxcloud/internal/github"
	"github.com/maxemiliang/maxcloud/internal/kube"
)

func doctorCmd(g *globals) *cobra.Command {
	var f authFlags
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything mcloud needs is set up, and say how to fix what isn't",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancel()
			bad := 0
			line := func(ok bool, what, detail string) {
				mark := "ok  "
				if !ok {
					mark = "FIX "
					bad++
				}
				fmt.Fprintf(g.out, "%s %-10s %s\n", mark, what, detail)
			}
			note := func(what, detail string) { fmt.Fprintf(g.out, "--   %-10s %s\n", what, detail) }

			// The platform, and whether this mcloud is new enough for it.
			p, err := discover(ctx)
			switch {
			case err != nil:
				note("platform", fmt.Sprintf("no discovery document (%v)", err))
				line(true, "mcloud", version())
			case p.minVersion() != "" && olderThan(version(), p.minVersion()):
				line(false, "mcloud", fmt.Sprintf("%s is older than the platform wants (%s): mise up mcloud", version(), p.minVersion()))
			default:
				line(true, "mcloud", version())
				note("platform", p.APIServer)
			}

			// The login.
			t, err := auth.Load(f.config())
			switch {
			case err != nil:
				line(false, "login", err.Error())
			case t == nil:
				line(false, "login", "not logged in: mcloud login")
			case t.Valid(time.Now()):
				line(true, "login", fmt.Sprintf("%s until %s", t.User, t.Expiry.Local().Format("15:04")))
			case t.RefreshToken != "":
				line(true, "login", t.User+" (refreshed when next used; mcloud login if that fails)")
			default:
				line(false, "login", "expired: mcloud login")
			}

			// The cluster, as mcloud reaches it.
			path, why := kube.KubeconfigSource(g.kubeconfig)
			where := firstNonEmpty(path, why) + " (" + why + ")"
			if k, err := g.kube(); err != nil {
				line(false, "cluster", err.Error())
			} else if r, err := k.Core.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authv1.SelfSubjectReview{}, metav1.CreateOptions{}); err != nil {
				line(false, "cluster", fmt.Sprintf("%s: %v", where, err))
			} else {
				line(true, "cluster", fmt.Sprintf("%s as %s", where, r.Status.UserInfo.Username))
			}

			// GitHub, for following GitOps deploys.
			if _, err := github.Token(); err != nil {
				line(false, "github", err.Error()+" (GitOps deploys follow the build on GitHub: gh auth login)")
			} else {
				line(true, "github", "token found")
			}

			// Tools for building here (direct deploys, mcloud dev).
			tool := func(name, why string) {
				if p, err := exec.LookPath(name); err == nil {
					note(name, p)
				} else {
					note(name, "not installed: "+why)
				}
			}
			tool("docker", "direct deploys of apps with a Dockerfile, and mcloud dev's local Postgres")
			if packWorks(ctx) {
				note("pack", "works")
			} else {
				note("pack", "not installed (or a shim with no version): buildpacks run in a container instead (needs docker)")
			}

			// The app here, if any.
			if proj, err := loadProject(); err == nil {
				if k, err := g.kube(); err == nil {
					if st, err := k.AppStatus(ctx, proj.App); err == nil && st.Entry != nil {
						line(true, "app", proj.App+" (registered)")
					} else if err == nil {
						note("app", proj.App+" (not registered yet: mcloud deploy registers it)")
					}
				}
			}
			if bad > 0 {
				return errors.New("some things need fixing (FIX above)")
			}
			return nil
		},
	}
	f.bind(c)
	return c
}
