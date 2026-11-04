package naming

import (
	"strings"
	"testing"
)

func TestBucket(t *testing.T) {
	for _, tc := range []struct {
		app, name, want string
		wantErr         bool
	}{
		{app: "shop", name: "uploads", want: "mc-shop--uploads"},
		{app: "shop-x", name: "a-b-1", want: "mc-shop-x--a-b-1"},
		{app: "shop", name: "db", want: "mc-shop--db"},
		{app: "shop", name: "a--b", wantErr: true},
		{app: "shop--x", name: "uploads", wantErr: true},
		{app: "shop", name: "Uploads", wantErr: true},
		{app: "shop", name: "-uploads", wantErr: true},
		{app: "shop", name: "uploads-", wantErr: true},
		{app: "shop", name: "up.loads", wantErr: true},
		{app: "shop", name: "x-s3alias", wantErr: true},
		{app: "shop", name: "", wantErr: true},
		{app: "shop", name: strings.Repeat("a", 54), want: "mc-shop--" + strings.Repeat("a", 54)},
		{app: "shop", name: strings.Repeat("a", 55), wantErr: true},
	} {
		got, err := Bucket(tc.app, tc.name)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Bucket(%q, %q) = %q, want an error", tc.app, tc.name, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("Bucket(%q, %q) = %q, %v; want %q", tc.app, tc.name, got, err, tc.want)
		}
	}
}

// The point of "--": one app's prefix never matches another app's buckets.
func TestPrefixesDontOverlap(t *testing.T) {
	apps := []string{"shop", "shop-x", "shop-x-y", "s", "sh"}
	for _, a := range apps {
		for _, b := range apps {
			if a == b {
				continue
			}
			bucket, err := Bucket(b, "uploads")
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(bucket, BucketPrefix(a)) {
				t.Errorf("app %s's prefix %s matches app %s's bucket %s", a, BucketPrefix(a), b, bucket)
			}
		}
	}
}
