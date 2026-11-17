// Package naming derives the names of everything the controller creates in
// Object Storage from an app's name (its namespace).
//
// Everything of app "shop" is named mc-shop--<something>. App names and
// bucket names never contain "--", so the prefix "mc-shop--" can only match
// shop's own buckets. With a single hyphen, mc-shop-* would also match the
// buckets of an app called shop-x, and so would shop's S3 policy.
//
// An app's environments and previews are apps of their own, named
// <app>--<env> (shop--staging): exactly one "--", so their buckets are
// mc-shop--staging--<bucket>. Their prefix matches only their own buckets;
// shop's prefix matches theirs too, which shop's policy takes back with a
// Deny (s3policy.App).
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

// ValidateApp checks that an app name can be used in bucket and user names:
// an app (shop), or an app's environment (shop--staging).
func ValidateApp(app string) error {
	parent, env, isEnv := strings.Cut(app, sep)
	if !label.MatchString(parent) || (isEnv && !label.MatchString(env)) {
		return fmt.Errorf("app %q: only lowercase letters, digits and single hyphens (an environment: <app>--<env>)", app)
	}
	return nil
}

// SplitEnvironment splits an environment's name, shop--staging, into its
// app and environment; an app's own name has no environment.
func SplitEnvironment(name string) (app, env string, ok bool) {
	return strings.Cut(name, sep)
}

// Environment is the name of an app's environment or preview: its
// registry entry, namespace and S3 user.
func Environment(app, env string) string { return app + sep + env }

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
