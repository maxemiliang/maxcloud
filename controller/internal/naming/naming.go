// Package naming derives the names of everything the controller creates in
// Object Storage from an app's name (its namespace).
//
// Everything of app "shop" is named mc-shop--<something>. App names and
// bucket names never contain "--", so the prefix "mc-shop--" can only match
// shop's own buckets. With a single hyphen, mc-shop-* would also match the
// buckets of an app called shop-x, and so would shop's S3 policy.
package naming

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	prefix = "mc-"
	sep    = "--"

	// SecretName is the Secret in the app's namespace with its S3
	// credentials and endpoint.
	SecretName = "maxcloud-s3"

	// DatabaseBucket is the bucket name (after the app prefix) that holds
	// the app's database backups. Not allowed as a Bucket's name.
	DatabaseBucket = "db"

	// S3 bucket names are at most 63 characters.
	maxBucketName = 63
)

// label: lowercase letters and digits, single hyphens between them.
var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9]|-[a-z0-9])*$`)

// ValidateApp checks that an app name can be used in bucket and user names.
func ValidateApp(app string) error {
	if !label.MatchString(app) {
		return fmt.Errorf("app %q: only lowercase letters, digits and single hyphens", app)
	}
	return nil
}

// Bucket returns the Object Storage bucket name of an app's bucket.
func Bucket(app, name string) (string, error) {
	if err := ValidateApp(app); err != nil {
		return "", err
	}
	if !label.MatchString(name) {
		return "", fmt.Errorf("bucket %q: only lowercase letters, digits and single hyphens", name)
	}
	// Reserved by S3 for access point aliases.
	if strings.HasSuffix(name, "-s3alias") {
		return "", fmt.Errorf("bucket %q: names ending in -s3alias are reserved", name)
	}
	b := BucketPrefix(app) + name
	if len(b) > maxBucketName {
		return "", fmt.Errorf("bucket %q of app %q: %s is %d characters, S3 allows %d", name, app, b, len(b), maxBucketName)
	}
	return b, nil
}

// BucketPrefix is what every bucket of the app starts with.
func BucketPrefix(app string) string {
	return prefix + app + sep
}

// User is the app's S3 user, and also its policy's name.
func User(app string) string {
	return prefix + app
}
