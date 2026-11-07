// Package objectstorage is what the controller does in UpCloud Managed
// Object Storage: through UpCloud's API (buckets, users, policies, access
// keys) and through the S3 API (bucket policies, objects).
//
// Every method is idempotent: creating what exists and deleting what's gone
// both succeed, so a reconcile can always start from the top.
package objectstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
)

// Storage is the part of UpCloud's API the controller uses, for one Object
// Storage instance.
type Storage interface {
	// Buckets returns every bucket in the instance with its metrics.
	Buckets(ctx context.Context) (map[string]BucketMetrics, error)
	EnsureBucket(ctx context.Context, name string) error
	// DeleteBucket deletes the bucket and every object in it.
	DeleteBucket(ctx context.Context, name string) error

	EnsureUser(ctx context.Context, username string) error
	// DeleteUser deletes the user with its access keys and policy
	// attachments.
	DeleteUser(ctx context.Context, username string) error

	// EnsurePolicy creates the policy, or makes document its default
	// version if it says something else.
	EnsurePolicy(ctx context.Context, name, description, document string) error
	DeletePolicy(ctx context.Context, name string) error
	EnsureUserPolicy(ctx context.Context, username, policy string) error

	// AccessKeys lists the IDs of the user's access keys.
	AccessKeys(ctx context.Context, username string) ([]string, error)
	CreateAccessKey(ctx context.Context, username string) (id, secret string, err error)
	DeleteAccessKey(ctx context.Context, username, id string) error
}

// BucketPolicies is the part of the S3 API the Bucket controller uses.
type BucketPolicies interface {
	PutBucketPolicy(ctx context.Context, bucket, document string) error
	// DeleteBucketPolicy succeeds if the bucket has no policy.
	DeleteBucketPolicy(ctx context.Context, bucket string) error
}

// Objects is the part of the S3 API the Database controller uses: the
// restore marker, and looking at what backups a bucket holds.
type Objects interface {
	// GetObject returns the object, or found false if there's none. An
	// object bigger than MaxObjectBytes is an error (ErrTooLarge).
	GetObject(ctx context.Context, bucket, key string) (data []byte, found bool, err error)
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	// DeleteObject succeeds if the object is already gone.
	DeleteObject(ctx context.Context, bucket, key string) error
	// Keys lists up to max keys under prefix ("" for the whole bucket).
	// Both return ErrNoSuchBucket if the bucket doesn't exist.
	Keys(ctx context.Context, bucket, prefix string, max int32) ([]string, error)
}

type BucketMetrics struct {
	SizeBytes int64
	Objects   int64
}

// statusError is implemented by the AWS SDK's API errors.
type statusError interface{ HTTPStatusCode() int }

// httpStatus is the HTTP status of an UpCloud or S3 API error, 0 if it has
// none.
func httpStatus(err error) int {
	var p *upcloud.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	var s statusError
	if errors.As(err, &s) {
		return s.HTTPStatusCode()
	}
	return 0
}

// sameDocument reports whether two policy documents, each URL-encoded or
// not, say the same thing.
func sameDocument(a, b string) (bool, error) {
	ja, err := canonicalJSON(a)
	if err != nil {
		return false, err
	}
	jb, err := canonicalJSON(b)
	if err != nil {
		return false, err
	}
	return ja == jb, nil
}

func canonicalJSON(doc string) (string, error) {
	if decoded, err := url.QueryUnescape(doc); err == nil {
		doc = decoded
	}
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		return "", fmt.Errorf("policy document isn't JSON: %w", err)
	}
	out, err := json.Marshal(v) // map keys come out sorted
	return string(out), err
}

func isNotFound(err error) bool { return httpStatus(err) == http.StatusNotFound }

// alreadyExists reports whether a create failed because the thing is
// already there. UpCloud's API answers that with 400 and an invalid
// parameter like `(root): "Bucket already exists"` (seen 2026-11-05), not
// with 409; S3 uses 409.
func alreadyExists(err error) bool {
	if httpStatus(err) == http.StatusConflict {
		return true
	}
	var p *upcloud.Problem
	if errors.As(err, &p) && p.Status == http.StatusBadRequest {
		for _, ip := range p.InvalidParams {
			if strings.Contains(strings.ToLower(ip.Reason), "already exists") {
				return true
			}
		}
		return strings.Contains(strings.ToLower(p.Title), "already exists")
	}
	return false
}
