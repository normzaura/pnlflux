package util

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client wraps an S3 client, target bucket, and region.
type S3Client struct {
	client *s3.Client
	bucket string
	region string
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
