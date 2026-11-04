package objectstorage

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3 implements BucketPolicies with the S3 API, as the controller's own S3
// user.
type S3 struct {
	client *s3.Client
}

// NewS3 returns an S3 client for UpCloud Object Storage.
func NewS3(endpoint, region, accessKeyID, secretAccessKey string) *S3 {
	c := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		UsePathStyle: true,
		// UpCloud rejects the checksums newer AWS SDKs send by default
		// (homelab docs/kb/data/upcloud-s3-checksums.md).
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3{client: c}
}

func (s *S3) PutBucketPolicy(ctx context.Context, bucket, document string) error {
	_, err := s.client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(bucket),
		Policy: aws.String(document),
	})
	if err != nil {
		return fmt.Errorf("put policy on bucket %s: %w", bucket, err)
	}
	return nil
}

func (s *S3) DeleteBucketPolicy(ctx context.Context, bucket string) error {
	_, err := s.client.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(bucket)})
	if err != nil && !isNotFound(err) && !strings.Contains(err.Error(), "NoSuchBucketPolicy") {
		return fmt.Errorf("delete policy of bucket %s: %w", bucket, err)
	}
	return nil
}
