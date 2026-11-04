package objectstorage

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
)

func TestSameDocument(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[{"Sid":"A","Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::b/*"]}]}`
	reordered := `{"Statement": [{"Resource": ["arn:aws:s3:::b/*"], "Action": ["s3:GetObject"], "Effect": "Allow", "Sid": "A"}], "Version": "2012-10-17"}`
	for name, stored := range map[string]string{
		"plain":             doc,
		"encoded":           url.QueryEscape(doc),
		"reordered":         reordered,
		"reordered encoded": url.QueryEscape(reordered),
	} {
		same, err := sameDocument(stored, url.QueryEscape(doc))
		if err != nil || !same {
			t.Errorf("%s: sameDocument = %v, %v; want true", name, same, err)
		}
	}
	other := `{"Version":"2012-10-17","Statement":[]}`
	if same, err := sameDocument(other, doc); err != nil || same {
		t.Errorf("different documents: sameDocument = %v, %v; want false", same, err)
	}
	if _, err := sameDocument("not json", doc); err == nil {
		t.Error("sameDocument(not json) should fail")
	}
}

type awsLikeError struct{ code int }

func (e awsLikeError) Error() string       { return "aws" }
func (e awsLikeError) HTTPStatusCode() int { return e.code }

func TestHTTPStatus(t *testing.T) {
	notFound := fmt.Errorf("get user: %w", &upcloud.Problem{Status: http.StatusNotFound})
	if !isNotFound(notFound) || alreadyExists(notFound) {
		t.Error("a wrapped UpCloud 404 should be not found")
	}
	if !alreadyExists(fmt.Errorf("x: %w", awsLikeError{http.StatusConflict})) {
		t.Error("a wrapped S3 409 means it exists")
	}
	// What UpCloud's API really answers for an existing bucket.
	exists := &upcloud.Problem{
		Status:        http.StatusBadRequest,
		Title:         "Validation error.",
		InvalidParams: []upcloud.ProblemInvalidParam{{Name: "(root)", Reason: `"Bucket already exists".`}},
	}
	if !alreadyExists(fmt.Errorf("create bucket: %w", exists)) {
		t.Error("UpCloud's 400 \"Bucket already exists\" means it exists")
	}
	invalid := &upcloud.Problem{
		Status:        http.StatusBadRequest,
		Title:         "Validation error.",
		InvalidParams: []upcloud.ProblemInvalidParam{{Name: "name", Reason: "invalid bucket name"}},
	}
	if alreadyExists(invalid) {
		t.Error("another 400 doesn't mean it exists")
	}
	if httpStatus(fmt.Errorf("plain")) != 0 {
		t.Error("a plain error has no status")
	}
}
