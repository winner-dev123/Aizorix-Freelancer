// Package storage abstracts the object store (S3 in prod, MinIO in dev). Only presigned
// URLs are produced here — the encrypted blob travels directly between the device/client
// and S3, never through this service.
package storage

import (
	"context"
	"time"
)

// Presigner issues short-TTL presigned PUT/GET URLs and verifies object integrity.
type Presigner interface {
	// PresignPut returns a URL the device PUTs ciphertext to, plus headers it must send
	// (e.g. SSE-KMS, content-type). TTL should be short (a few minutes).
	PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (url string, headers map[string]string, err error)
	// PresignGet returns a short-TTL download URL for an authorized viewer.
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)
	// HeadSHA256 fetches the object's server-side checksum for async integrity re-verification.
	HeadSHA256(ctx context.Context, bucket, key string) ([]byte, error)
	// Delete removes the object (compliance erasure / retention enforcement).
	Delete(ctx context.Context, bucket, key string) error
}

// StubPresigner is a deterministic local implementation used in dev/tests. In production,
// AWSPresigner (s3presign) lives behind a build tag so the AWS SDK isn't pulled into tests.
type StubPresigner struct{ Endpoint string }

func (s StubPresigner) PresignPut(_ context.Context, bucket, key string, ttl time.Duration) (string, map[string]string, error) {
	url := s.Endpoint + "/" + bucket + "/" + key + "?X-Amz-Expires=" + itoa(int(ttl.Seconds()))
	headers := map[string]string{
		"x-amz-server-side-encryption":                "aws:kms",
		"x-amz-server-side-encryption-aws-kms-key-id": "alias/aizorix-screenshots",
		"content-type": "application/octet-stream",
	}
	return url, headers, nil
}
func (s StubPresigner) PresignGet(_ context.Context, bucket, key string, ttl time.Duration) (string, error) {
	return s.Endpoint + "/" + bucket + "/" + key + "?X-Amz-Expires=" + itoa(int(ttl.Seconds())), nil
}
func (s StubPresigner) HeadSHA256(_ context.Context, _, _ string) ([]byte, error) { return nil, nil }
func (s StubPresigner) Delete(_ context.Context, _, _ string) error               { return nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
