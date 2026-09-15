package storage

import (
	"net/url"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

func newTestS3Signer(t *testing.T) *URLSignerS3 {
	t.Helper()
	cfg := &config.Config{Storage: config.StorageConfig{}}
	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String("us-east-1"),
		Credentials: credentials.NewStaticCredentials("AKIDEXAMPLE", "secret-key", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := s3.New(sess)
	st := &S3Storage{
		Client:   client,
		Bucket:   "test-bucket",
		BasePath: "uploads",
	}
	return NewURLSignerS3(st, cfg)
}

func TestS3SignerRejectsUnsignedRequests(t *testing.T) {
	signer := newTestS3Signer(t)
	query := url.Values{}
	if signer.IsValid("users/1/photo.jpg", query) {
		t.Fatal("unsigned request must be rejected; IsValid previously returned true unconditionally")
	}
}

func TestS3SignerAcceptsOwnPresignature(t *testing.T) {
	signer := newTestS3Signer(t)
	signed, err := signer.Sign("users/1/photo.jpg")
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if !signer.IsValid("users/1/photo.jpg", parsed.Query()) {
		t.Fatal("a freshly presigned URL must validate")
	}
}

func TestS3SignerRejectsTamperedPathAndExpiredURL(t *testing.T) {
	signer := newTestS3Signer(t)
	signed, err := signer.Sign("users/1/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(signed)

	// Signature bound to a different path must not validate.
	if signer.IsValid("users/2/other.jpg", parsed.Query()) {
		t.Fatal("presignature must be bound to the signed object key")
	}

	// Expired presigned URL must be rejected locally.
	q := parsed.Query()
	q.Set("X-Amz-Date", time.Now().UTC().Add(-24*time.Hour).Format("20060102T150405Z"))
	if signer.IsValid("users/1/photo.jpg", q) {
		t.Fatal("expired presigned URL must be rejected")
	}
}
