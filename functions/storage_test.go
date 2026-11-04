package functions

import "testing"

func TestBucketName(t *testing.T) {
	env := func(app string) func(string) string {
		return func(k string) string {
			if k == "MAXCLOUD_APP" {
				return app
			}
			return ""
		}
	}
	if got, err := bucketName("uploads", env("shop")); err != nil || got != "mc-shop--uploads" {
		t.Errorf("bucketName = %q, %v", got, err)
	}
	if _, err := bucketName("uploads", env("")); err == nil {
		t.Error("no MAXCLOUD_APP: want an error")
	}
	for _, bad := range []string{"a--b", "Up", "-x", "x-", "a.b", ""} {
		if _, err := bucketName(bad, env("shop")); err == nil {
			t.Errorf("bucketName(%q): want an error", bad)
		}
	}
}
