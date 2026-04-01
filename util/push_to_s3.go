package util

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client wraps an S3 client, target bucket, and region.
type S3Client struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
	region        string
}

// PushToS3 uploads data to the configured S3 bucket under the given key.
// If an object with the same key already exists, it is renamed to <name>(old).<ext>
// before uploading. If <name>(old).<ext> also exists, (old1), (old2), etc. are tried.
func (c *S3Client) PushToS3(ctx context.Context, key string, data []byte) (string, error) {
	if c.objectExists(ctx, key) {
		if err := c.archiveExisting(ctx, key); err != nil {
			return "", fmt.Errorf("archive existing %q: %w", key, err)
		}
	}

	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"),
	})
	if err != nil {
		return "", fmt.Errorf("s3 put object %q: %w", key, err)
	}
	presigned, err := c.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(7*24*time.Hour))
	if err != nil {
		return "", fmt.Errorf("presign %q: %w", key, err)
	}
	return presigned.URL, nil
}

// objectExists returns true if the key exists in the bucket.
func (c *S3Client) objectExists(ctx context.Context, key string) bool {
	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return err == nil
}

// archiveExisting renames the object at key to <base>(old).<ext>,
// incrementing to (old1), (old2), etc. if needed.
func (c *S3Client) archiveExisting(ctx context.Context, key string) error {
	ext := ""
	if i := strings.LastIndex(key, "."); i >= 0 {
		ext = key[i:]
		key = key[:i]
	}

	archiveKey := key + "(old)" + ext
	for i := 1; c.objectExists(ctx, archiveKey); i++ {
		archiveKey = fmt.Sprintf("%s(old%d)%s", key, i, ext)
	}

	_, err := c.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(c.bucket),
		CopySource: aws.String(c.bucket + "/" + key + ext),
		Key:        aws.String(archiveKey),
	})
	if err != nil {
		return fmt.Errorf("copy to %q: %w", archiveKey, err)
	}

	_, err = c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key + ext),
	})
	if err != nil {
		return fmt.Errorf("delete original %q: %w", key+ext, err)
	}
	return nil
}
