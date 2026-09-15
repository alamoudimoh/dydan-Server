package storage

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// Integration test against a real S3-compatible backend (MinIO). Skipped
// unless DYDAN_TEST_S3_ENDPOINT is set, e.g.:
//
//	DYDAN_TEST_S3_ENDPOINT=127.0.0.1:9000 \
//	DYDAN_TEST_S3_ACCESS=minioadmin DYDAN_TEST_S3_SECRET=minioadmin \
//	go test ./internal/storage/ -run TestS3Integration
func newMinioStorage(t *testing.T) (*S3Storage, *s3.S3) {
	t.Helper()
	endpoint := cmp.Or(
		os.Getenv("DYDAN_TEST_S3_ENDPOINT"),
		os.Getenv("DAYDAN_TEST_S3_ENDPOINT"),
	)
	if endpoint == "" {
		t.Skip("DYDAN_TEST_S3_ENDPOINT not set; skipping S3 integration test")
	}
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String("us-east-1"),
		Endpoint:         aws.String("http://" + endpoint),
		Credentials:      awsCredentialsFromEnv(),
		S3ForcePathStyle: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := s3.New(sess)
	for _, bucket := range []string{"private-bucket", "public-bucket"} {
		if _, err := client.CreateBucket(&s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			if !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") &&
				!strings.Contains(err.Error(), "BucketAlreadyExists") {
				t.Fatalf("create bucket %s: %v", bucket, err)
			}
		}
	}
	st := &S3Storage{
		Bucket:       "private-bucket",
		PublicBucket: "public-bucket",
		BasePath:     "uploads",
		Client:       client,
	}
	return st, client
}

func awsCredentialsFromEnv() *credentials.Credentials {
	return credentials.NewStaticCredentials(
		cmp.Or(os.Getenv("DYDAN_TEST_S3_ACCESS"), os.Getenv("DAYDAN_TEST_S3_ACCESS")),
		cmp.Or(os.Getenv("DYDAN_TEST_S3_SECRET"), os.Getenv("DAYDAN_TEST_S3_SECRET")),
		"",
	)
}

// TestS3IntegrationObjectIdentityAndDeletion proves:
//   - Save/Delete/presign all derive the same bucket key from BasePath+path;
//     a delete actually removes the saved object (previously the raw
//     concatenation missed the separator and deletes silently no-op'd);
//   - DeletePublicByURL removes objects from the dedicated public bucket
//     instead of leaving anonymously-readable orphans in place;
//   - path-style presigned URLs validate through the local verifier.
func TestS3IntegrationObjectIdentityAndDeletion(t *testing.T) {
	st, client := newMinioStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := &config.Config{Storage: config.StorageConfig{
		BucketName:       st.Bucket,
		PublicBucketName: st.PublicBucket,
		BasePath:         st.BasePath,
	}}
	signer := NewURLSignerS3(st, cfg)

	// 1. Save + signed URL validates locally.
	if err := st.Save(ctx, "users/9/report.pdf", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("save: %v", err)
	}
	signed, err := signer.Sign("users/9/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if !signer.IsValid("users/9/report.pdf", parsed.Query()) {
		t.Fatal("path-style presigned URL must validate")
	}

	// 2. Delete removes the exact object Save wrote.
	if err := st.Delete(ctx, []string{"users/9/report.pdf"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(st.Bucket), Key: aws.String("uploads/users/9/report.pdf"),
	}); err == nil {
		t.Fatal("object still present after Delete: key derivation mismatch")
	}

	// 3. Public bucket deletion targets the public bucket.
	if err := st.SavePublic(ctx, "profiles/1/avatar.jpg", bytes.NewReader([]byte("jpg"))); err != nil {
		t.Fatalf("save public: %v", err)
	}
	publicURL, err := st.GetPublicURL(ctx, "profiles/1/avatar.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(publicURL, "profiles/1/avatar.jpg") {
		t.Fatalf("unexpected public URL %q", publicURL)
	}
	if err := st.DeletePublicByURL(ctx, publicURL); err != nil {
		t.Fatalf("delete public by url: %v", err)
	}
	if _, err := client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(st.PublicBucket), Key: aws.String("uploads/profiles/1/avatar.jpg"),
	}); err == nil {
		t.Fatal("public object still anonymously readable after DeletePublicByURL")
	}

	// 4. External URLs are ignored.
	if err := st.DeletePublicByURL(ctx, "https://some-cdn.example.com/other/x.jpg"); err != nil {
		t.Fatalf("external URL must be ignored: %v", err)
	}

	fmt.Fprintln(os.Stderr, "s3 integration assertions passed")
}
