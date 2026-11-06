package functions

import (
	"fmt"
	"os"
	"regexp"
)

var bucketRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9]|-[a-z0-9])*$`)

// BucketName is the full name of one of the app's buckets:
// BucketName("uploads") is "mc-shop--uploads" in app shop.
//
// An app asks for buckets in its maxcloud.yaml (buckets: [uploads]). The
// maxcloud controller puts the app's credentials in the Secret maxcloud-s3,
// which every function gets as the AWS_* environment variables the AWS SDK
// reads by itself (endpoint, region, keys, UpCloud's checksum settings).
// MAXCLOUD_APP, the app's name, is set on every function. With
// aws-sdk-go-v2:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
//	bucket, err := functions.BucketName("uploads")
//	...
//	s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), ...})
func BucketName(name string) (string, error) {
	return bucketName(name, os.Getenv)
}

func bucketName(name string, getenv func(string) string) (string, error) {
	app := getenv("MAXCLOUD_APP")
	if app == "" {
		return "", fmt.Errorf("bucket %q: MAXCLOUD_APP isn't set, so this isn't running on maxcloud (mcloud dev sets it locally)", name)
	}
	if !bucketRe.MatchString(name) {
		return "", fmt.Errorf("bucket %q: lowercase letters, digits and single dashes", name)
	}
	return "mc-" + app + "--" + name, nil
}
