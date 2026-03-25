package util

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client wraps an S3 client, target bucket, and region.
type S3Client struct {
	client *s3.Client
	bucket string
	region string
}

// NewS3Client loads AWS credentials from environment variables
// (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION) and returns an S3Client.
// AWS_S3_BUCKET must also be set.
func NewS3Client(ctx context.Context, bucket string) (*S3Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &S3Client{
		client: s3.NewFromConfig(cfg),
		bucket: bucket,
		region: cfg.Region,
	}, nil
}

// PushToS3 uploads data to the configured S3 bucket under the given key
// and returns the public object URL.
func (c *S3Client) PushToS3(ctx context.Context, key string, data []byte) (string, error) {
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"),
	})
	if err != nil {
		return "", fmt.Errorf("s3 put object %q: %w", key, err)
	}
	url := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", c.bucket, c.region, key)
	return url, nil
}
