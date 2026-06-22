package storage

import (
	"context"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Presigner is the production object-store presigner (S3-compatible: AWS S3 or MinIO in
// dev). The encrypted blob travels directly between the device/client and the bucket via
// these short-TTL presigned URLs; the service never proxies the bytes.
type S3Presigner struct {
	client *minio.Client
}

func NewS3Presigner(endpoint, accessKey, secretKey string, useSSL bool) (*S3Presigner, error) {
	c, err := minio.New(stripScheme(endpoint), &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}
	return &S3Presigner{client: c}, nil
}

func (s *S3Presigner) PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (string, map[string]string, error) {
	u, err := s.client.PresignedPutObject(ctx, bucket, key, ttl)
	if err != nil {
		return "", nil, err
	}
	// A presigned PUT requires no extra headers (the V4 signature covers the canonical
	// request). For production AWS with SSE-KMS, the encryption headers would be signed in here.
	return u.String(), map[string]string{}, nil
}

func (s *S3Presigner) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, bucket, key, ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// HeadSHA256 would StatObject and compare the stored checksum during async integrity
// re-verification; left as a no-op in the scaffold (the device sha256 + signature are the
// primary integrity controls, verified at confirm time).
func (s *S3Presigner) HeadSHA256(ctx context.Context, bucket, key string) ([]byte, error) {
	return nil, nil
}

func (s *S3Presigner) Delete(ctx context.Context, bucket, key string) error {
	return s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return endpoint
}
