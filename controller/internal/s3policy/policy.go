// Package s3policy builds the IAM-style policy documents the controller puts
// on Object Storage: each app user's policy, and the bucket policy of a
// public bucket.
package s3policy

import (
	"encoding/json"
	"net/url"

	"github.com/maxemiliang/maxcloud/controller/internal/naming"
)

// Document is an AWS-format policy document.
type Document struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

type Statement struct {
	Sid       string   `json:"Sid"`
	Effect    string   `json:"Effect"`
	Principal any      `json:"Principal,omitempty"`
	Action    []string `json:"Action"`
	Resource  []string `json:"Resource"`
}

const version = "2012-10-17"

func arn(resource string) string { return "arn:aws:s3:::" + resource }

// MarkerKey is the object in an app's database bucket that records which
// backup generation is current. The controller writes it; the app must not.
const MarkerKey = "maxcloud.json"

// App is the policy of an app's S3 user: its own buckets' objects and
// nothing else.
//
// It can't create, delete or reconfigure buckets (no CreateBucket,
// DeleteBucket, PutBucketPolicy): what buckets an app has, and whether
// they're public, is the registry's call. It can't touch the restore marker
// in its database bucket either; the database's backups themselves it must
// be able to delete, since the backup plugin enforces retention with the
// same credentials.
func App(app string) Document {
	buckets := arn(naming.BucketPrefix(app) + "*")
	marker := arn(naming.BucketPrefix(app) + naming.DatabaseBucket + "/" + MarkerKey)
	return Document{
		Version: version,
		Statement: []Statement{
			{
				Sid:      "ListOwnBuckets",
				Effect:   "Allow",
				Action:   []string{"s3:ListBucket", "s3:GetBucketLocation", "s3:ListBucketMultipartUploads"},
				Resource: []string{buckets},
			},
			{
				Sid:    "ReadWriteOwnObjects",
				Effect: "Allow",
				Action: []string{
					"s3:GetObject", "s3:PutObject", "s3:DeleteObject",
					"s3:AbortMultipartUpload", "s3:ListMultipartUploadParts",
				},
				Resource: []string{buckets + "/*"},
			},
			{
				Sid:      "KeepOffTheRestoreMarker",
				Effect:   "Deny",
				Action:   []string{"s3:PutObject", "s3:DeleteObject"},
				Resource: []string{marker},
			},
		},
	}
}

// PublicRead is the bucket policy of a public bucket: anyone may get its
// objects. Listing still needs credentials.
func PublicRead(bucket string) Document {
	return Document{
		Version: version,
		Statement: []Statement{{
			Sid:       "PublicRead",
			Effect:    "Allow",
			Principal: "*",
			Action:    []string{"s3:GetObject"},
			Resource:  []string{arn(bucket + "/*")},
		}},
	}
}

// JSON is the document as compact JSON, for the S3 API.
func (d Document) JSON() string {
	b, err := json.Marshal(d)
	if err != nil {
		panic(err) // only strings and slices: can't fail
	}
	return string(b)
}

// Encoded is the document the way UpCloud's API takes it: URL-encoded JSON.
func (d Document) Encoded() string {
	return url.QueryEscape(d.JSON())
}
