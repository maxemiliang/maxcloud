// Package storage reads and writes the objects in an app's buckets, with
// the app's own S3 credentials (the Secret maxcloud-s3 the controller keeps
// in its namespace).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Client is an S3 client for one app's buckets.
type Client struct {
	// App is the app's name; its buckets are mc-<app>--<name>.
	App string
	s3  *s3.Client
}

// Object is one object in a bucket.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// New makes a client from the app's maxcloud-s3 Secret (the AWS_* variables
// its functions get).
func New(app string, secret map[string]string) (*Client, error) {
	id, key, endpoint := secret["AWS_ACCESS_KEY_ID"], secret["AWS_SECRET_ACCESS_KEY"], secret["AWS_ENDPOINT_URL_S3"]
	if id == "" || key == "" || endpoint == "" {
		return nil, fmt.Errorf("app %s has no bucket credentials: it gets them with its first bucket (buckets: in maxcloud.yaml), and loses them with its last", app)
	}
	region := secret["AWS_REGION"]
	if region == "" {
		region = "us-east-1"
	}
	return &Client{App: app, s3: s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(id, key, ""),
		UsePathStyle: true,
		// UpCloud Object Storage rejects the checksums newer AWS SDKs send
		// by default (platform/docs/kb/data/upcloud-s3-checksums.md).
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})}, nil
}

// Bucket is a bucket's full name in Object Storage: mc-<app>--<name>.
func (c *Client) Bucket(name string) string { return "mc-" + c.App + "--" + name }

// List calls fn for every object under prefix in the bucket, in key order.
// Without recursive it stops at the next "/": deeper keys come once, as a
// folder (an Object whose Key ends in "/", with no size).
func (c *Client) List(ctx context.Context, name, prefix string, recursive bool, fn func(Object) error) error {
	in := &s3.ListObjectsV2Input{Bucket: aws.String(c.Bucket(name)), Prefix: aws.String(prefix)}
	if !recursive {
		in.Delimiter = aws.String("/")
	}
	for {
		out, err := c.s3.ListObjectsV2(ctx, in)
		if err != nil {
			return c.explain(err, name)
		}
		for _, p := range out.CommonPrefixes {
			if err := fn(Object{Key: aws.ToString(p.Prefix)}); err != nil {
				return err
			}
		}
		for _, o := range out.Contents {
			if err := fn(Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), Modified: aws.ToTime(o.LastModified)}); err != nil {
				return err
			}
		}
		if !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			return nil
		}
		in.ContinuationToken = out.NextContinuationToken
	}
}

// Upload puts a local file at key. The content type comes from the file's
// extension, as a browser fetching it from a public bucket expects.
func (c *Client) Upload(ctx context.Context, name, key, file string) (int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.IsDir() {
		return 0, fmt.Errorf("%s is a directory (-r copies directories)", file)
	}
	in := &s3.PutObjectInput{
		Bucket:        aws.String(c.Bucket(name)),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(st.Size()),
	}
	if ct := mime.TypeByExtension(filepath.Ext(file)); ct != "" {
		in.ContentType = aws.String(ct)
	}
	if _, err := c.s3.PutObject(ctx, in); err != nil {
		return 0, c.explain(err, name)
	}
	return st.Size(), nil
}

// Download writes the object at key to a local file (through a temporary
// file next to it, so a failed download leaves nothing half-written), or to
// w if file is "".
func (c *Client) Download(ctx context.Context, name, key, file string, w io.Writer) (int64, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Bucket(name)), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return 0, fmt.Errorf("no object %s in bucket %s", key, name)
		}
		return 0, c.explain(err, name)
	}
	defer out.Body.Close()
	if file == "" {
		return io.Copy(w, out.Body)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".mcloud-*")
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(tmp, out.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return 0, err
	}
	return n, nil
}

// Delete removes the objects at keys, 1000 a request.
func (c *Client) Delete(ctx context.Context, name string, keys []string) error {
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		ids := make([]types.ObjectIdentifier, n)
		for i, k := range keys[:n] {
			ids[i] = types.ObjectIdentifier{Key: aws.String(k)}
		}
		out, err := c.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.Bucket(name)),
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return c.explain(err, name)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return fmt.Errorf("deleting %s from bucket %s: %s (%d of %d not deleted)", aws.ToString(e.Key), name, aws.ToString(e.Message), len(out.Errors), n)
		}
		keys = keys[n:]
	}
	return nil
}

// explain turns S3's answers into what to do about them.
func (c *Client) explain(err error, name string) error {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchBucket":
			return fmt.Errorf("the app %s has no bucket %s (mcloud bucket ls lists them)", c.App, name)
		case "AccessDenied":
			return fmt.Errorf("bucket %s: access denied: %s's credentials only reach its own buckets (%s*)", name, c.App, c.Bucket(""))
		}
		return fmt.Errorf("bucket %s: %s: %s", name, api.ErrorCode(), api.ErrorMessage())
	}
	return fmt.Errorf("bucket %s: %w", name, err)
}

// Ref is a bucket and a key (or key prefix) in it: "uploads:img/a.png".
type Ref struct {
	Bucket, Key string
}

// ParseRef splits BUCKET:KEY. ok is false for anything that isn't a bucket
// reference: a local path. A bucket's name is lowercase letters, digits and
// dashes, so "./a:b", "C:\x" and "/tmp/a:b" stay local.
func ParseRef(s string) (ref Ref, ok bool) {
	name, key, found := strings.Cut(s, ":")
	if !found || name == "" {
		return Ref{}, false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return Ref{}, false
		}
	}
	return Ref{Bucket: name, Key: strings.TrimPrefix(key, "/")}, true
}

func (r Ref) String() string { return r.Bucket + ":" + r.Key }

// Join is the key of a file named rel (slash-separated) under r: a key
// that is empty or ends in "/" is a folder.
func (r Ref) Join(rel string) string {
	if r.Key == "" || strings.HasSuffix(r.Key, "/") {
		return r.Key + rel
	}
	return r.Key + "/" + rel
}

// Base is the last part of a key: the file name it downloads to.
func Base(key string) string { return path.Base(key) }
