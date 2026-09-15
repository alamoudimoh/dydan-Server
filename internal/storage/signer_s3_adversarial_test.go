package storage

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// TestS3SignerRejectsTamperedSigV4Parameters covers forged/oversized/future
// SigV4 parameters through the local presignature verifier.
func TestS3SignerRejectsTamperedSigV4Parameters(t *testing.T) {
	signer := newTestS3Signer(t)
	signed, err := signer.Sign("users/1/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}

	// Excessive X-Amz-Expires (beyond the 7-day SigV4 ceiling).
	q := parsed.Query()
	q.Set("X-Amz-Expires", strconv.Itoa(60*60*24*30))
	if signer.IsValid("users/1/photo.jpg", q) {
		t.Fatal("X-Amz-Expires beyond the SigV4 ceiling must be rejected")
	}

	// Zero / negative / malformed expiry.
	for _, bad := range []string{"0", "-60", "abc", ""} {
		q.Set("X-Amz-Expires", bad)
		if signer.IsValid("users/1/photo.jpg", q) {
			t.Fatalf("X-Amz-Expires %q must be rejected", bad)
		}
	}
	q = parsed.Query()

	// Future-dated signature: extends effective validity past any intended
	// lifetime and must be refused.
	q.Set("X-Amz-Date", time.Now().UTC().Add(time.Hour).Format("20060102T150405Z"))
	if signer.IsValid("users/1/photo.jpg", q) {
		t.Fatal("future-dated X-Amz-Date must be rejected")
	}
	q = parsed.Query()

	// Unsupported algorithm / signed headers.
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA1")
	if signer.IsValid("users/1/photo.jpg", q) {
		t.Fatal("unsupported algorithm must be rejected")
	}
	q = parsed.Query()
	q.Set("X-Amz-SignedHeaders", "host;x-amz-date")
	if signer.IsValid("users/1/photo.jpg", q) {
		t.Fatal("multi-header signed headers must be rejected")
	}
	q = parsed.Query()

	// Credential scope tampering.
	q.Set("X-Amz-Credential", "AKIDEXAMPLE/20260825/us-east-1/ec2/aws4_request")
	if signer.IsValid("users/1/photo.jpg", q) {
		t.Fatal("wrong service scope must be rejected")
	}
	q = parsed.Query()
	q.Set("X-Amz-Credential", "OTHERKEY/20260825/us-east-1/s3/aws4_request")
	if signer.IsValid("users/1/photo.jpg", q) {
		t.Fatal("unknown access key must be rejected")
	}
}

// TestS3SignerTTLClampAndConfig proves the minted lifetime follows the
// configured TTL and is clamped to the AWS ceiling.
func newTestS3Storage(t *testing.T) *S3Storage {
	t.Helper()
	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String("us-east-1"),
		Credentials: credentials.NewStaticCredentials("AKIDEXAMPLE", "secret-key", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &S3Storage{Client: s3.New(sess), Bucket: "test-bucket", BasePath: "uploads"}
}

func TestS3SignerTTLClampAndConfig(t *testing.T) {
	st := newTestS3Storage(t)

	short := NewURLSignerS3(st, s3CfgWithTTL(time.Hour))
	signed, err := short.Sign("users/1/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(signed)
	expiresIn := presignLifetimeSeconds(t, parsed)
	if expiresIn != 3600 {
		t.Fatalf("expected configured 1h lifetime, got %ds", expiresIn)
	}

	over := NewURLSignerS3(st, s3CfgWithTTL(30*24*time.Hour))
	signed, err = over.Sign("users/1/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ = url.Parse(signed)
	if got := presignLifetimeSeconds(t, parsed); got != int64(MaxPresignTTL/time.Second) {
		t.Fatalf("TTL must clamp to the SigV4 ceiling, got %ds", got)
	}

	zero := NewURLSignerS3(st, s3CfgWithTTL(0))
	signed, err = zero.Sign("users/1/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ = url.Parse(signed)
	if got := presignLifetimeSeconds(t, parsed); got <= 0 {
		t.Fatalf("non-positive configured TTL must fall back to the ceiling, got %ds", got)
	}
}

func s3CfgWithTTL(ttl time.Duration) *config.Config {
	return &config.Config{Storage: config.StorageConfig{SignedURLTTL: ttl}}
}

func presignLifetimeSeconds(t *testing.T, parsed *url.URL) int64 {
	t.Helper()
	q := parsed.Query()
	date, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	if err != nil {
		t.Fatal(err)
	}
	expSecs, err := strconv.ParseInt(q.Get("X-Amz-Expires"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return int64(date.Add(time.Duration(expSecs) * time.Second).Sub(date).Seconds())
}

// TestS3PublicReadModeSemantics documents public_read behavior: unsigned URL
// minting plus fail-open verification scoped to that explicit mode only.
func TestS3PublicReadModeSemantics(t *testing.T) {
	st := newTestS3Storage(t)

	private := &config.Config{Storage: config.StorageConfig{PublicHost: "", PublicRead: false}}
	signerPrivate := NewURLSignerS3(st, private)
	if signerPrivate.PublicRead {
		t.Fatal("public_read must default to false")
	}
	signed, err := signerPrivate.Sign("users/1/x.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(signed, "X-Amz-Signature") {
		t.Fatal("private mode must mint signed URLs")
	}
	q, _ := url.ParseQuery("nothing=here")
	if signerPrivate.IsValid("users/1/x.jpg", q) {
		t.Fatal("private mode must reject unsigned requests")
	}

	public := &config.Config{Storage: config.StorageConfig{
		PublicBucketName: "public-bucket",
		PublicBucketHost: "cdn.example.com",
		PublicRead:       true,
	}}
	signerPublic := NewURLSignerS3(st, public)
	if !signerPublic.PublicRead {
		t.Fatal("explicit public_read=true must be honored")
	}
	unsigned, err := signerPublic.Sign("profiles/1/x.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unsigned, "X-Amz-Signature") {
		t.Fatal("public-read mode must return unsigned URLs")
	}
}
