package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxemiliang/maxcloud/internal/functions"
	"github.com/maxemiliang/maxcloud/internal/storage"
)

func bucketCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:     "bucket",
		Aliases: []string{"buckets"},
		Short:   "An app's buckets: list them, and list, copy and remove their objects",
		Long: `Look into an app's S3 buckets (buckets: in maxcloud.yaml) and move files
in and out of them, with the app's own credentials: the ones its functions
get, which reach the app's buckets and nothing else. Only the app's owners
and admins can read them.

An object is named BUCKET:KEY, with the bucket's name from maxcloud.yaml:
uploads:img/cat.png is the object img/cat.png in mc-<app>--uploads.
BUCKET: is the whole bucket, BUCKET:img/ everything under img/.

Run these in the app's repo, or name the app with --app.`,
	}
	c.AddCommand(bucketLsCmd(g), bucketCpCmd(g), bucketRmCmd(g))
	return c
}

// bucketClient is the S3 client for the app's buckets, from its
// maxcloud-s3 Secret.
func (g *globals) bucketClient(ctx context.Context, app string) (*storage.Client, error) {
	if g.testStorage != nil {
		return g.testStorage(app)
	}
	k, err := g.kube()
	if err != nil {
		return nil, err
	}
	secret, err := k.SecretData(ctx, app, functions.StorageSecret)
	if err != nil {
		return nil, fmt.Errorf("reading %s's bucket credentials: %w", app, err)
	}
	return storage.New(app, secret)
}

func bucketLsCmd(g *globals) *cobra.Command {
	var app string
	var recursive, asJSON bool
	c := &cobra.Command{
		Use:   "ls [BUCKET[:PREFIX]]",
		Short: "List the app's buckets, or the objects in one",
		Long: `Without an argument: the app's buckets, with their size and, for public
ones, where their objects are served. The sizes are the maxcloud
controller's, a few minutes old.

With BUCKET or BUCKET:PREFIX: the objects there, one level deep (folders
end in /); -r lists everything below.

A bucket that was removed from maxcloud.yaml isn't in the first list any
more, but it and its objects are still there, and still stored: its
objects can be listed by its name (mcloud bucket ls old:), and removed
with mcloud bucket rm -r old:.`,
		Example: `  mcloud bucket ls
  mcloud bucket ls uploads
  mcloud bucket ls uploads:img/ -r`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName([]string{app})
			if err != nil {
				return err
			}
			u := g.UI()
			if len(args) == 0 {
				k, err := g.kube()
				if err != nil {
					return err
				}
				buckets, err := k.AppBuckets(cmd.Context(), name)
				if err != nil {
					return err
				}
				if asJSON {
					out := struct {
						App     string       `json:"app"`
						Buckets []bucketJSON `json:"buckets"`
					}{App: name, Buckets: []bucketJSON{}}
					for _, b := range buckets {
						j := bucketJSON{Name: b.Name, Bucket: b.FullName, Public: b.Public, URL: b.URL, Ready: condOf(b.Ready)}
						if b.Size >= 0 {
							j.SizeBytes = &b.Size
						}
						if b.Objects >= 0 {
							j.Objects = &b.Objects
						}
						out.Buckets = append(out.Buckets, j)
					}
					return g.printJSON(out)
				}
				if len(buckets) == 0 {
					u.Note("%s has no buckets (buckets: [uploads] in maxcloud.yaml gives it one)", name)
					return nil
				}
				var rows [][]string
				for _, b := range buckets {
					ready := u.Mark(b.Ready.Status, true)
					if !b.Ready.Ready() && b.Ready.Message != "" {
						ready += " " + truncate(b.Ready.Message, 60)
					}
					size, objects := "", ""
					if b.Size >= 0 {
						size = sizeString(b.Size)
					}
					if b.Objects >= 0 {
						objects = fmt.Sprint(b.Objects)
					}
					url := u.Dim("private")
					if b.URL != "" {
						url = u.Link(b.URL)
					} else if b.Public {
						url = u.Dim("public")
					}
					rows = append(rows, []string{u.Accent(b.Name), size, objects, url, ready})
				}
				u.Table([]string{"Bucket", "Size", "Objects", "Served at", "Ready"}, rows)
				return nil
			}
			ref, err := bucketRef(args[0])
			if err != nil {
				return err
			}
			s3, err := g.bucketClient(cmd.Context(), name)
			if err != nil {
				return err
			}
			if asJSON {
				out := struct {
					App     string       `json:"app"`
					Bucket  string       `json:"bucket"`
					Prefix  string       `json:"prefix"`
					Objects []objectJSON `json:"objects"`
				}{App: name, Bucket: ref.Bucket, Prefix: ref.Key, Objects: []objectJSON{}}
				err = s3.List(cmd.Context(), ref.Bucket, ref.Key, recursive, func(o storage.Object) error {
					j := objectJSON{Key: o.Key, Folder: o.Modified.IsZero() && strings.HasSuffix(o.Key, "/")}
					if !j.Folder {
						j.SizeBytes, j.Modified = &o.Size, rfc3339(o.Modified)
					}
					out.Objects = append(out.Objects, j)
					return nil
				})
				if err != nil {
					return err
				}
				return g.printJSON(out)
			}
			n := 0
			err = s3.List(cmd.Context(), ref.Bucket, ref.Key, recursive, func(o storage.Object) error {
				n++
				if strings.HasSuffix(o.Key, "/") && o.Size == 0 && o.Modified.IsZero() {
					fmt.Fprintf(g.out, "%10s  %-16s  %s\n", "", "", o.Key)
					return nil
				}
				fmt.Fprintf(g.out, "%10s  %-16s  %s\n", sizeString(o.Size), o.Modified.Local().Format("2006-01-02 15:04"), o.Key)
				return nil
			})
			if err != nil {
				return err
			}
			if n == 0 {
				u.Note("nothing at %s", ref)
			}
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "the app (default: $MCLOUD_APP, else maxcloud.yaml's)")
	c.Flags().BoolVarP(&recursive, "recursive", "r", false, "list everything below the prefix, not one level")
	jsonFlag(c, &asJSON)
	return c
}

func bucketCpCmd(g *globals) *cobra.Command {
	var app string
	var recursive bool
	c := &cobra.Command{
		Use:   "cp SOURCE DEST",
		Short: "Copy files into a bucket, or objects out of one",
		Long: `Copy between this machine and one of the app's buckets. One side is a
local path and the other BUCKET:KEY.

Into a bucket: a file becomes the object KEY, or keeps its name under KEY
when that's empty or ends in /. The content type is set from the file's
extension. -r copies a directory, with its files' paths below it as keys.

Out of a bucket: an object is written to DEST, or into it when DEST is a
directory; - writes it to stdout. -r copies everything under the prefix
into the directory DEST.

Existing objects and files are overwritten. A public bucket is cached by
Cloudflare: give changed files new keys.`,
		Example: `  mcloud bucket cp logo.png assets:
  mcloud bucket cp -r dist/ assets:app/
  mcloud bucket cp uploads:img/cat.png .
  mcloud bucket cp -r uploads:img/ ./backup/
  mcloud bucket cp uploads:report.csv - | head`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName([]string{app})
			if err != nil {
				return err
			}
			src, srcRemote := storage.ParseRef(args[0])
			dst, dstRemote := storage.ParseRef(args[1])
			switch {
			case srcRemote && dstRemote:
				return errors.New("one side has to be a local path: copy out of one bucket, then into the other (a local file whose name looks like BUCKET:KEY is ./BUCKET:KEY)")
			case !srcRemote && !dstRemote:
				return errors.New("one side has to be BUCKET:KEY, e.g. uploads:img/cat.png (uploads: is the whole bucket)")
			}
			s3, err := g.bucketClient(cmd.Context(), name)
			if err != nil {
				return err
			}
			var files int
			var bytes int64
			if dstRemote {
				files, bytes, err = bucketUpload(cmd.Context(), g, s3, args[0], dst, recursive)
			} else {
				files, bytes, err = bucketDownload(cmd.Context(), g, s3, src, args[1], recursive)
			}
			if err != nil {
				return err
			}
			if args[1] != "-" {
				g.UI().Success("Copied %s (%s)", plural(files, "file"), sizeString(bytes))
			}
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "the app (default: $MCLOUD_APP, else maxcloud.yaml's)")
	c.Flags().BoolVarP(&recursive, "recursive", "r", false, "copy a directory, or everything under a prefix")
	return c
}

func bucketUpload(ctx context.Context, g *globals, s3 *storage.Client, local string, dst storage.Ref, recursive bool) (int, int64, error) {
	st, err := os.Stat(local)
	if err != nil {
		return 0, 0, err
	}
	if !st.IsDir() {
		key := dst.Key
		if key == "" || strings.HasSuffix(key, "/") {
			key += filepath.Base(local)
		}
		n, err := s3.Upload(ctx, dst.Bucket, key, local)
		return 1, n, err
	}
	if !recursive {
		return 0, 0, fmt.Errorf("%s is a directory: -r copies it", local)
	}
	var files int
	var bytes int64
	err = filepath.WalkDir(local, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, sockets: not something a bucket holds
		}
		rel, err := filepath.Rel(local, p)
		if err != nil {
			return err
		}
		key := dst.Join(filepath.ToSlash(rel))
		n, err := s3.Upload(ctx, dst.Bucket, key, p)
		if err != nil {
			return err
		}
		files++
		bytes += n
		g.UI().Printf("%s", g.UI().Dim(dst.Bucket+":"+key))
		return nil
	})
	return files, bytes, err
}

func bucketDownload(ctx context.Context, g *globals, s3 *storage.Client, src storage.Ref, local string, recursive bool) (int, int64, error) {
	if !recursive {
		if src.Key == "" || strings.HasSuffix(src.Key, "/") {
			return 0, 0, fmt.Errorf("%s is a folder: -r copies everything under it", src)
		}
		if local == "-" {
			n, err := s3.Download(ctx, src.Bucket, src.Key, "", g.out)
			return 1, n, err
		}
		if st, err := os.Stat(local); (err == nil && st.IsDir()) || strings.HasSuffix(local, string(filepath.Separator)) {
			local = filepath.Join(local, storage.Base(src.Key))
		}
		n, err := s3.Download(ctx, src.Bucket, src.Key, local, nil)
		return 1, n, err
	}
	if local == "-" {
		return 0, 0, errors.New("-r copies into a directory, not to stdout")
	}
	prefix := src.Key
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var keys []string
	if err := s3.List(ctx, src.Bucket, prefix, true, func(o storage.Object) error {
		if !strings.HasSuffix(o.Key, "/") { // a "folder" marker object holds nothing
			keys = append(keys, o.Key)
		}
		return nil
	}); err != nil {
		return 0, 0, err
	}
	if len(keys) == 0 {
		return 0, 0, fmt.Errorf("nothing under %s:%s", src.Bucket, prefix)
	}
	var bytes int64
	for _, key := range keys {
		rel := strings.TrimPrefix(key, prefix)
		// A key is whatever its writer chose: one with .. in it mustn't
		// land outside the directory asked for.
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return 0, 0, fmt.Errorf("object %s:%s has a key that isn't a path below %s: copy it by itself, to a name of your choice", src.Bucket, key, local)
		}
		n, err := s3.Download(ctx, src.Bucket, key, filepath.Join(local, filepath.FromSlash(rel)), nil)
		if err != nil {
			return 0, 0, err
		}
		bytes += n
		g.UI().Printf("%s", g.UI().Dim(filepath.Join(local, filepath.FromSlash(rel))))
	}
	return len(keys), bytes, nil
}

func bucketRmCmd(g *globals) *cobra.Command {
	var app string
	var recursive, yes bool
	c := &cobra.Command{
		Use:   "rm BUCKET:KEY...",
		Short: "Remove objects from a bucket",
		Long: `Remove objects. Without -r each argument is one object's key. With -r it's
a prefix, and everything under it goes: BUCKET:img/ the folder, BUCKET:
every object in the bucket. -r says how many objects and how much that
is, and asks first (-y doesn't).

There's no undo: buckets keep no old versions.

It also empties a bucket that was removed from maxcloud.yaml (removing it
there keeps the bucket and what's in it): mcloud bucket rm -r old:. What
remains is an empty bucket, which stores nothing.`,
		Example: `  mcloud bucket rm uploads:img/cat.png
  mcloud bucket rm -r uploads:tmp/
  mcloud bucket rm -r old:`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName([]string{app})
			if err != nil {
				return err
			}
			var refs []storage.Ref
			for _, a := range args {
				ref, ok := storage.ParseRef(a)
				if !ok {
					return fmt.Errorf("%s isn't BUCKET:KEY", a)
				}
				if !recursive && (ref.Key == "" || strings.HasSuffix(ref.Key, "/")) {
					return fmt.Errorf("%s is a folder: -r removes everything under it", ref)
				}
				refs = append(refs, ref)
			}
			s3, err := g.bucketClient(cmd.Context(), name)
			if err != nil {
				return err
			}
			u := g.UI()
			if !recursive {
				byBucket := map[string][]string{}
				for _, r := range refs {
					byBucket[r.Bucket] = append(byBucket[r.Bucket], r.Key)
				}
				for bucket, keys := range byBucket {
					if err := s3.Delete(cmd.Context(), bucket, keys); err != nil {
						return err
					}
				}
				u.Success("Removed %s", plural(len(refs), "object"))
				return nil
			}
			for _, ref := range refs {
				var keys []string
				var bytes int64
				if err := s3.List(cmd.Context(), ref.Bucket, ref.Key, true, func(o storage.Object) error {
					keys = append(keys, o.Key)
					bytes += o.Size
					return nil
				}); err != nil {
					return err
				}
				if len(keys) == 0 {
					u.Note("nothing at %s", ref)
					continue
				}
				what := fmt.Sprintf("%s (%s) under %s", plural(len(keys), "object"), sizeString(bytes), ref)
				if ref.Key == "" {
					what = fmt.Sprintf("all %s (%s) in %s's bucket %s", plural(len(keys), "object"), sizeString(bytes), name, ref.Bucket)
				}
				if !yes {
					ok, err := g.confirm("Remove " + what + "? There's no undo.")
					if err != nil {
						return fmt.Errorf("%w (-y removes without asking)", err)
					}
					if !ok {
						u.Note("left %s alone", ref)
						continue
					}
				}
				if err := s3.Delete(cmd.Context(), ref.Bucket, keys); err != nil {
					return err
				}
				u.Success("Removed %s", what)
			}
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "the app (default: $MCLOUD_APP, else maxcloud.yaml's)")
	c.Flags().BoolVarP(&recursive, "recursive", "r", false, "remove everything under the prefix (BUCKET: is the whole bucket)")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "with -r: don't ask first")
	return c
}

// objectJSON is one object, or a folder one level down (mcloud bucket ls
// --json).
type objectJSON struct {
	Key       string `json:"key"`
	Folder    bool   `json:"folder"`
	SizeBytes *int64 `json:"sizeBytes"`
	Modified  any    `json:"modified"`
}

// bucketRef reads BUCKET or BUCKET:PREFIX.
func bucketRef(s string) (storage.Ref, error) {
	if !strings.Contains(s, ":") {
		s += ":"
	}
	ref, ok := storage.ParseRef(s)
	if !ok {
		return storage.Ref{}, fmt.Errorf("%s isn't a bucket's name (lowercase letters, digits and dashes) or BUCKET:PREFIX", strings.TrimSuffix(s, ":"))
	}
	return ref, nil
}

// sizeString is a byte count for people: 512 B, 1.4 MiB.
func sizeString(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
