package s3policy

import (
	"net/url"
	"strings"
	"testing"
)

func TestApp(t *testing.T) {
	d := App("shop")
	got := d.JSON()
	for _, want := range []string{
		`"arn:aws:s3:::mc-shop--*"`,
		`"arn:aws:s3:::mc-shop--*/*"`,
		`"Effect":"Deny"`,
		`"arn:aws:s3:::mc-shop--db/maxcloud.json"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("App policy lacks %s:\n%s", want, got)
		}
	}
	for _, never := range []string{"s3:*", "CreateBucket", "DeleteBucket\"", "PutBucketPolicy", "mc-shop-*"} {
		if strings.Contains(got, never) {
			t.Errorf("App policy contains %s:\n%s", never, got)
		}
	}
	// UpCloud's policy size limit.
	if n := len(d.Encoded()); n > 6144 {
		t.Errorf("encoded policy is %d characters, UpCloud allows 6144", n)
	}
	if dec, err := url.QueryUnescape(d.Encoded()); err != nil || dec != got {
		t.Errorf("Encoded doesn't decode back to the JSON: %v", err)
	}
}

func TestPublicRead(t *testing.T) {
	got := PublicRead("mc-shop--assets").JSON()
	want := `{"Version":"2012-10-17","Statement":[{"Sid":"PublicRead","Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::mc-shop--assets/*"]}]}`
	if got != want {
		t.Errorf("PublicRead =\n%s\nwant\n%s", got, want)
	}
}
