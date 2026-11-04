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
// An app's buckets come from the registry (mcloud apps set <app> --buckets
// uploads). The maxcloud controller puts the app's credentials in the
// Secret maxcloud-s3, which every function gets as environment variables:
// the AWS_* ones the AWS SDK reads by itself (endpoint, region, keys,
// UpCloud's checksum settings) and MAXCLOUD_APP. With aws-sdk-go-v2:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
//	s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(functions.BucketName("uploads")), ...})
func BucketName(name string) (string, error) {
	return bucketName(name, os.Getenv)
}

func bucketName(name string, getenv func(string) string) (string, error) {
	app := getenv("MAXCLOUD_APP")
	if app == "" {
		return "", fmt.Errorf("bucket %q: MAXCLOUD_APP isn't set: the app has no buckets yet (mcloud apps set <app> --buckets ...), or this isn't running on maxcloud", name)
	}
	if !bucketRe.MatchString(name) {
		return "", fmt.Errorf("bucket %q: lowercase letters, digits and single dashes", name)
	}
	return "mc-" + app + "--" + name, nil
}
