//go:build integration

// Checks what the controller assumes about UpCloud Managed Object Storage,
// against a real instance. It creates an app "it<random>" with buckets,
// user, policy and key, tries what the app may and may not do, and deletes
// everything again.
//
//	UPCLOUD_TOKEN=... MAXCLOUD_OBJECT_STORAGE_ID=... MAXCLOUD_S3_ENDPOINT=https://... \
//	AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... \
//	go test -tags integration -run TestUpCloud -v ./internal/objectstorage/
//
// The AWS_* keys are the controller's own S3 user. Nothing secret is logged.
package objectstorage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/maxemiliang/maxcloud/controller/internal/naming"
	"github.com/maxemiliang/maxcloud/controller/internal/s3policy"
)

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set", name)
	}
	return v
}

// eventually retries f for a while: new keys and policies take a moment to
// apply.
func eventually(t *testing.T, what string, f func() error) {
	t.Helper()
	var err error
	for range 20 {
		if err = f(); err == nil {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("%s: %v", what, err)
}

func forbidden(err error) bool { return httpStatus(err) == http.StatusForbidden }

// mustBeDenied fails unless f is refused with 403 (retried, in case a
// policy isn't in force yet).
func mustBeDenied(t *testing.T, what string, f func() error) {
	t.Helper()
	eventually(t, what+" should be denied", func() error {
		err := f()
		if forbidden(err) {
			return nil
		}
		if err == nil {
			return errors.New("it was allowed")
		}
		return err
	})
	t.Logf("ok: %s is denied", what)
}

func anonymousGet(t *testing.T, u string) int {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Logf("GET %s: %v", u, err)
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestUpCloud(t *testing.T) {
	token := env(t, "UPCLOUD_TOKEN")
	service := env(t, "MAXCLOUD_OBJECT_STORAGE_ID")
	endpoint := strings.TrimRight(env(t, "MAXCLOUD_S3_ENDPOINT"), "/")
	region := os.Getenv("MAXCLOUD_S3_REGION")
	if region == "" {
		region = "europe-1"
	}
	ctrlS3 := NewS3(endpoint, region, env(t, "AWS_ACCESS_KEY_ID"), env(t, "AWS_SECRET_ACCESS_KEY"))
	st := NewUpCloud(token, service)
	ctx := context.Background()

	suffix := make([]byte, 3)
	rand.Read(suffix)
	app := "it" + hex.EncodeToString(suffix)
	user := naming.User(app)
	probe, _ := naming.Bucket(app, "probe")
	dbBucket, _ := naming.Bucket(app, naming.DatabaseBucket)
	other, _ := naming.Bucket(app+"-x", "other") // another app's bucket
	t.Logf("app %s, buckets %s %s %s", app, probe, dbBucket, other)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, b := range []string{probe, dbBucket, other} {
			if err := st.DeleteBucket(ctx, b); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
		if err := st.DeleteUser(ctx, user); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		if err := st.DeletePolicy(ctx, user); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	// --- UpCloud's API, each call twice: idempotent ---------------------
	for range 2 {
		for _, b := range []string{probe, dbBucket, other} {
			if err := st.EnsureBucket(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.EnsureUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		if err := st.EnsurePolicy(ctx, user, "maxcloud integration test", s3policy.App(app).Encoded()); err != nil {
			t.Fatal(err)
		}
		if err := st.EnsureUserPolicy(ctx, user, user); err != nil {
			t.Fatal(err)
		}
	}
	// A changed policy becomes a new version, then back: twice through the
	// update path.
	changed := s3policy.App(app)
	changed.Statement = changed.Statement[:2]
	for _, d := range []s3policy.Document{changed, s3policy.App(app)} {
		if err := st.EnsurePolicy(ctx, user, "maxcloud integration test", d.Encoded()); err != nil {
			t.Fatalf("policy update: %v", err)
		}
	}
	t.Log("ok: buckets, user, policy (with two updates) and attachment")

	buckets, err := st.Buckets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{probe, dbBucket, other} {
		if _, ok := buckets[b]; !ok {
			t.Errorf("Buckets() lacks %s", b)
		}
	}

	id, secret, err := st.CreateAccessKey(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := st.AccessKeys(ctx, user)
	if err != nil || !slices.Contains(keys, id) {
		t.Fatalf("AccessKeys = %v, %v; want the new key", keys, err)
	}
	t.Log("ok: access key created and listed")

	// --- What the app's key may do ---------------------------------------
	appS3 := NewS3(endpoint, region, id, secret).client
	put := func(c *s3.Client, bucket, key, body string) error {
		_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body)})
		return err
	}
	eventually(t, "app puts an object in its bucket", func() error { return put(appS3, probe, "hello.txt", "hello") })
	out, err := appS3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(probe), Key: aws.String("hello.txt")})
	if err != nil {
		t.Fatalf("app gets its object: %v", err)
	}
	body, _ := io.ReadAll(out.Body)
	out.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("got %q", body)
	}
	if _, err := appS3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(probe)}); err != nil {
		t.Fatalf("app lists its bucket: %v", err)
	}
	if err := put(appS3, dbBucket, "db-1/wals/0001", "wal"); err != nil {
		t.Fatalf("app writes a backup file: %v", err)
	}
	t.Log("ok: the app reads, writes and lists its own buckets")

	// --- What it may not -------------------------------------------------
	if err := put(ctrlS3.client, dbBucket, s3policy.MarkerKey, `{"generation":1}`); err != nil {
		t.Fatalf("controller writes the marker: %v", err)
	}
	mustBeDenied(t, "app overwriting the restore marker (Deny statement)", func() error {
		return put(appS3, dbBucket, s3policy.MarkerKey, `{"generation":99}`)
	})
	mustBeDenied(t, "app deleting the restore marker", func() error {
		_, err := appS3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(dbBucket), Key: aws.String(s3policy.MarkerKey)})
		return err
	})
	mustBeDenied(t, "app reading another app's bucket", func() error {
		_, err := appS3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(other)})
		return err
	})
	mustBeDenied(t, "app creating a bucket", func() error {
		evil, _ := naming.Bucket(app, "evil")
		_, err := appS3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(evil)})
		if err == nil {
			st.DeleteBucket(ctx, evil)
		}
		return err
	})
	mustBeDenied(t, "app making its bucket public", func() error {
		_, err := appS3.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(probe), Policy: aws.String(s3policy.PublicRead(probe).JSON())})
		return err
	})

	// --- Presigned upload, as a browser would do it ----------------------
	presigned, err := s3.NewPresignClient(appS3).PresignPutObject(ctx,
		&s3.PutObjectInput{Bucket: aws.String(probe), Key: aws.String("upload.txt")},
		s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, presigned.URL, bytes.NewReader([]byte("from a browser")))
	for k, vs := range presigned.SignedHeader {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("presigned PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned PUT: %s", resp.Status)
	}
	t.Log("ok: presigned PUT")

	// --- Public read -----------------------------------------------------
	host := strings.TrimPrefix(endpoint, "https://")
	pathURL := endpoint + "/" + probe + "/hello.txt"
	vhostURL := "https://" + probe + "." + host + "/hello.txt"
	if code := anonymousGet(t, pathURL); code != http.StatusForbidden {
		t.Errorf("anonymous GET before the bucket is public: %d, want 403", code)
	}
	if err := ctrlS3.PutBucketPolicy(ctx, probe, s3policy.PublicRead(probe).JSON()); err != nil {
		t.Fatalf("public-read bucket policy: %v", err)
	}
	eventually(t, "anonymous GET of a public object (path style)", func() error {
		if code := anonymousGet(t, pathURL); code != http.StatusOK {
			return errors.New(http.StatusText(code))
		}
		return nil
	})
	t.Logf("ok: public read, path style: %s", pathURL)
	// Informational: browsers and CDNs prefer virtual-hosted URLs.
	t.Logf("virtual-hosted %s: HTTP %d", vhostURL, anonymousGet(t, vhostURL))
	if code := anonymousGet(t, endpoint+"/"+probe+"?list-type=2"); code == http.StatusOK {
		t.Error("anonymous listing of a public bucket is allowed; only GetObject should be")
	}
	if err := ctrlS3.DeleteBucketPolicy(ctx, probe); err != nil {
		t.Fatal(err)
	}
	if err := ctrlS3.DeleteBucketPolicy(ctx, probe); err != nil {
		t.Fatalf("deleting a policy that's gone: %v", err)
	}
	eventually(t, "anonymous GET denied again", func() error {
		if code := anonymousGet(t, pathURL); code != http.StatusForbidden {
			return errors.New(http.StatusText(code))
		}
		return nil
	})
	t.Log("ok: private again")

	// --- Key rotation and cleanup paths ----------------------------------
	if err := st.DeleteAccessKey(ctx, user, id); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAccessKey(ctx, user, id); err != nil {
		t.Fatalf("deleting a key that's gone: %v", err)
	}
	u, _ := url.Parse(endpoint)
	t.Logf("done against %s", u.Host)
}
