package objectstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3 implements BucketPolicies and Objects with the S3 API, as the
// controller's own S3 user.
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

func (s *S3) GetObject(ctx context.Context, bucket, key string) ([]byte, bool, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if noSuchBucket(err) {
		return nil, false, ErrNoSuchBucket
	}
	var noKey *types.NoSuchKey
	if errors.As(err, &noKey) || isNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get s3://%s/%s: %w", bucket, key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read s3://%s/%s: %w", bucket, key, err)
	}
	return data, true, nil
}

func (s *S3) PutObject(ctx context.Context, bucket, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}

func (s *S3) Keys(ctx context.Context, bucket, prefix string, max int32) ([]string, error) {
	in := &s3.ListObjectsV2Input{Bucket: aws.String(bucket), MaxKeys: aws.Int32(max)}
	if prefix != "" {
		in.Prefix = aws.String(prefix)
	}
	out, err := s.client.ListObjectsV2(ctx, in)
	if noSuchBucket(err) {
		return nil, ErrNoSuchBucket
	}
	if err != nil {
		return nil, fmt.Errorf("list s3://%s/%s: %w", bucket, prefix, err)
	}
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	return keys, nil
}

// ErrNoSuchBucket: the bucket doesn't exist.
var ErrNoSuchBucket = errors.New("no such bucket")

func noSuchBucket(err error) bool {
	var nb *types.NoSuchBucket
	if errors.As(err, &nb) {
		return true
	}
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "NoSuchBucket"
}

func (s *S3) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}
