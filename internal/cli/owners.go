package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/registry"
)

// me is the Authelia username mcloud reaches the cluster as (after mcloud
// login), or "" when it reaches it as something else, such as an admin's
// certificate from upctl.
func (g *globals) me(ctx context.Context, k *kube.Client) (string, error) {
	user, err := k.Whoami(ctx)
	if err != nil {
		return "", fmt.Errorf("finding out who you are on the cluster: %w", err)
	}
	name, ok := strings.CutPrefix(user, kube.AutheliaPrefix)
	if !ok {
		return "", nil
	}
	return name, nil
}

// addOwner makes the user registering an app one of its owners; admission
// refuses a developer's new entry without them. An admin on a certificate
// has no username: the app then has only the owners given (maybe none,
// which leaves it to admins).
func (g *globals) addOwner(ctx context.Context, k *kube.Client, e *registry.Entry) error {
	me, err := g.me(ctx, k)
	if err != nil {
		return err
	}
	owners, err := registry.ParseOwners(e.Owners)
	if err != nil {
		return err
	}
	if me != "" && !slices.Contains(owners, me) {
		owners = append([]string{me}, owners...)
	}
	e.Owners = registry.FormatOwners(owners)
	if e.Owners == "" {
		g.UI().Warn("you aren't logged in through Authelia (mcloud login), so %s gets no owners: only admins can change it", e.Name)
	}
	return nil
}

// changeOwners is cur with add added and remove removed. An app keeps at
// least one owner.
func changeOwners(cur, add, remove []string) ([]string, error) {
	out := slices.Clone(cur)
	for _, o := range add {
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	for _, o := range remove {
		if !slices.Contains(out, o) {
			return nil, fmt.Errorf("%s isn't an owner", o)
		}
		out = slices.DeleteFunc(out, func(x string) bool { return x == o })
	}
	if len(out) == 0 {
		return nil, errors.New("an app needs at least one owner: add the new owner before removing the last one")
	}
	return out, nil
}

func appsOwnersCmd(g *globals) *cobra.Command {
	var add, remove []string
	var cf confirmFlags
	c := &cobra.Command{
		Use:   "owners NAME",
		Short: "Show an app's owners, or add and remove co-owners",
		Long: `Show who owns an app, or change it. An app's owners are Authelia users:
each is admin in the app's namespace (its pods, Secrets, database), and only
they (and admins) may change or remove the app. Whoever registers an app
owns it; owners add the people they run it with.

Other developers see that the app exists, not its data. An app keeps at
least one owner: to hand one over, add the new owner, then remove yourself.
Owners never come from maxcloud.yaml.`,
		Example: `  mcloud apps owners shop
  mcloud apps owners shop --add alice
  mcloud apps owners shop --add alice --remove bob`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()
			k, err := g.kube()
			if err != nil {
				return err
			}
			cur, err := k.GetRegistryEntry(ctx, name)
			if err != nil {
				return err
			}
			if cur == nil {
				return fmt.Errorf("%s isn't registered (mcloud apps list)", name)
			}
			u := g.UI()
			owners := cur.Entry.OwnerList()
			if len(add) == 0 && len(remove) == 0 {
				if len(owners) == 0 {
					u.Printf("%s has no owners: only admins can change it", u.Bold(name))
				} else {
					u.Printf("%s is owned by %s", u.Bold(name), strings.Join(owners, ", "))
				}
				if cur.Registration != "" {
					u.Hint("registration %s", cur.Registration)
				}
				return nil
			}
			if cur.InGit {
				return fmt.Errorf("%s is registered in git: its owners change in platform/apps/maxcloud/apps/%s.yaml in the maxcloud repo", name, name)
			}
			for _, o := range append(slices.Clone(add), remove...) {
				if _, err := registry.ParseOwners(o); err != nil || strings.Contains(o, ",") {
					return fmt.Errorf("owner %q: one Authelia username per --add or --remove", o)
				}
			}
			next, err := changeOwners(owners, add, remove)
			if err != nil {
				return err
			}
			e := *cur.Entry
			e.Owners = registry.FormatOwners(next)
			if err := e.Validate(); err != nil {
				return err
			}
			if me, err := g.me(ctx, k); err == nil && me != "" && slices.Contains(owners, me) && !slices.Contains(next, me) {
				u.Warn("you remove yourself: you can't change %s or see its data afterwards", name)
			}
			return g.changeApp(ctx, k, cur.Entry, &e, cf)
		},
	}
	c.Flags().StringArrayVar(&add, "add", nil, "an Authelia username to make an owner (repeatable)")
	c.Flags().StringArrayVar(&remove, "remove", nil, "an owner to remove (repeatable)")
	cf.bind(c.Flags(), false)
	return c
}
