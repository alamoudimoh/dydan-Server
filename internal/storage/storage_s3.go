package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

type S3Storage struct {
	Bucket       string
	PublicBucket string
	PublicHost   string
	BasePath     string
	Client       *s3.S3
	Key          string

	// opTimeout is the per-operation deadline applied by the storage client
	// itself (centralized policy, not scattered per-handler timers). It caps
	// every PutObject/DeleteObject round trip — including the SDK's bounded
	// retries — so a stalled S3/MinIO endpoint cannot hang a request or job
	// indefinitely. Caller cancellation still takes precedence: whichever
	// fires first wins.
	opTimeout time.Duration
}

// DefaultS3OperationTimeout bounds each S3 API operation (including retries).
// It is deliberately generous relative to the product's MaxFileSize so large
// uploads on slow links are not cut off mid-flight, but finite so a stalled
// connection always releases the caller.
const DefaultS3OperationTimeout = 2 * time.Minute

// s3MaxRetries is the explicit ceiling on AWS SDK retry attempts per
// operation. The SDK default already retries only on retryable errors with
// exponential backoff; pinning it documents the worst case instead of
// inheriting silent SDK changes.
const s3MaxRetries = 3

// MaxPresignTTL is the hard ceiling for S3 presigned URL lifetime: AWS SigV4
// rejects anything longer than 7 days. Runtime-minted presignatures use
// config.Storage.SignedURLTTL clamped to this value; persisted-URL fallbacks
// (GetPublicURL without a public host) keep the maximum because the URL is
// stored in a DB row and re-minted only on profile updates.
const MaxPresignTTL = 7 * 24 * time.Hour

// VALID_FOR is retained as an alias of MaxPresignTTL for existing callers.
const VALID_FOR = MaxPresignTTL

// objectKey joins BasePath and a relative storage path into a bucket key.
// Every code path that touches S3 (save, delete, sign, verify) must derive
// keys through this helper so path-style and virtual-host addressing always
// refer to the same object identity.
func (s *S3Storage) objectKey(path string) string {
	return fmt.Sprintf("%s/%s", s.BasePath, path)
}

func NewS3Storage(config *config.Config) (*S3Storage, error) {
	opTimeout := config.Storage.S3OperationTimeout
	if opTimeout <= 0 {
		opTimeout = DefaultS3OperationTimeout
	}
	sess, err := session.NewSession(&aws.Config{
		Region:   aws.String(config.Storage.Region),
		Endpoint: aws.String(config.Storage.Endpoint),
		Credentials: credentials.NewStaticCredentials(
			config.Storage.AccessKey,
			config.Storage.SecretKey,
			"",
		),
		S3ForcePathStyle: aws.Bool(config.Storage.PathStyle),
		// Bounded retry policy: at most s3MaxRetries attempts with the
		// SDK's standard exponential backoff. Combined with opTimeout this
		// guarantees every operation terminates in bounded time.
		MaxRetries: aws.Int(s3MaxRetries),
	})
	if err != nil {
		return nil, err
	}
	return &S3Storage{
		Bucket:       config.Storage.BucketName,
		PublicBucket: config.Storage.PublicBucketName,
		PublicHost:   config.Storage.PublicBucketHost,
		BasePath:     config.Storage.BasePath,
		Client:       s3.New(sess),
		opTimeout:    opTimeout,
	}, nil
}

// withOpTimeout derives a per-operation context from the caller's context.
// Caller cancellation propagates (ctx.Done closes immediately), and a hard
// deadline bounds stalled operations even when the caller passed no deadline.
func (s *S3Storage) withOpTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := s.opTimeout
	if timeout <= 0 {
		timeout = DefaultS3OperationTimeout
	}
	return context.WithTimeout(ctx, timeout)
}
func (s *S3Storage) Save(ctx context.Context, path string, file io.Reader) error {
	key := s.objectKey(path)
	ctx, cancel := s.withOpTimeout(ctx)
	defer cancel()

	// Read the file into a buffer to create an io.ReadSeeker
	buf, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(buf)

	_, err = s.Client.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
		Body:   reader,
	})
	if err != nil {
		return err
	}

	return nil
}
func (s *S3Storage) Delete(ctx context.Context, paths []string) error {
	var err error
	for _, path := range paths {
		// objectKey matches the key layout produced by Save; the previous
		// raw concatenation (BasePath + path) silently missed the separator
		// and deleted non-existent keys.
		key := s.objectKey(path)
		opCtx, cancel := s.withOpTimeout(ctx)
		_, e := s.Client.DeleteObjectWithContext(opCtx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.Bucket),
			Key:    aws.String(key),
		})
		cancel()
		if e != nil {
			err = e
		}
	}
	return err
}

func (s *S3Storage) GetURL(ctx context.Context, path string) (string, error) {
	key := s.objectKey(path)
	req, _ := s.Client.GetObjectRequest(&s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
	})
	// make this indefinite valid for 1000 years
	urlStr, err := req.Presign(VALID_FOR)
	if err != nil {
		return "", err
	}
	return urlStr, nil
}

func (s *S3Storage) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("Get method not implemented for S3Storage, use GetObject with the key")
}

func (s *S3Storage) publicBucket() string {
	if s.PublicBucket != "" {
		return s.PublicBucket
	}
	return s.Bucket
}

func (s *S3Storage) SavePublic(ctx context.Context, path string, file io.Reader) error {
	key := s.objectKey(path)
	ctx, cancel := s.withOpTimeout(ctx)
	defer cancel()
	buf, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	_, err = s.Client.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.publicBucket()),
		Key:    aws.String(key),
		Body:   bytes.NewReader(buf),
	})
	return err
}

func (s *S3Storage) GetPublicURL(ctx context.Context, path string) (string, error) {
	key := s.objectKey(path)
	if s.PublicHost != "" || s.PublicBucket != "" {
		// Objects written via SavePublic live in a deliberately-public
		// location; they are served by a bare (unsigned) URL. Presigning
		// them would be incoherent — the credential adds nothing on a
		// publicly readable bucket/prefix while imposing a 7-day expiry.
		return s.publicObjectURL(key), nil
	}
	// No public bucket configured — fall back to the private bucket presigned URL.
	return s.GetURL(ctx, path)
}

// publicObjectURL renders the bare URL for an object in the public location.
// With PublicBucketHost set it is a plain CDN URL; otherwise it points at the
// configured S3 endpoint honoring path-style / virtual-hosted addressing.
func (s *S3Storage) publicObjectURL(key string) string {
	if s.PublicHost != "" {
		return fmt.Sprintf("https://%s/%s", s.PublicHost, key)
	}
	endpoint := strings.TrimRight(aws.StringValue(s.Client.Config.Endpoint), "/")
	if endpoint == "" {
		return fmt.Sprintf("https://%s.s3.amazonaws.com/%s", s.publicBucket(), key)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return fmt.Sprintf("%s/%s/%s", endpoint, s.publicBucket(), key)
	}
	scheme := "https"
	if parsed.Scheme == "http" {
		scheme = "http"
	}
	if aws.BoolValue(s.Client.Config.S3ForcePathStyle) {
		return fmt.Sprintf("%s://%s/%s/%s", scheme, parsed.Host, s.publicBucket(), key)
	}
	return fmt.Sprintf("%s://%s.%s/%s", scheme, s.publicBucket(), parsed.Host, key)
}

// DeletePublicByURL deletes the S3 object identified by a public URL
// previously returned by GetPublicURL. Only URLs whose prefix matches this
// deployment's own public location(s) are acted on; all other URLs (external
// OIDC picture URLs, etc.) are ignored. Deletion targets the public bucket —
// deleting from the private bucket would leave the anonymously readable
// object in place.
func (s *S3Storage) DeletePublicByURL(ctx context.Context, rawURL string) error {
	if s.PublicHost == "" && s.PublicBucket == "" {
		return nil
	}
	candidates := []string{}
	if s.PublicHost != "" {
		candidates = append(candidates, fmt.Sprintf("https://%s/%s/", s.PublicHost, s.BasePath))
	}
	publicBase := s.publicObjectURL(strings.TrimSuffix(s.BasePath+"/", "/"))
	if publicBase != "" {
		candidates = append(candidates, publicBase+"/")
	}
	// Older deployments persisted presigned URLs minted against regional
	// AWS hosts; include those forms so stale objects are still matched.
	if aws.StringValue(s.Client.Config.Endpoint) == "" && s.PublicBucket != "" {
		if region := aws.StringValue(s.Client.Config.Region); region != "" {
			candidates = append(candidates,
				fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s/", s.PublicBucket, region, s.BasePath),
				fmt.Sprintf("https://%s.s3.amazonaws.com/%s/", s.PublicBucket, s.BasePath))
		}
	}
	var matchedPath string
	for _, prefix := range candidates {
		if strings.HasPrefix(rawURL, prefix) {
			matchedPath = strings.TrimPrefix(rawURL, prefix)
			break
		}
	}
	if matchedPath == "" {
		return nil
	}
	if s.PublicBucket == "" {
		return s.Delete(ctx, []string{matchedPath})
	}
	key := s.objectKey(matchedPath)
	opCtx, cancel := s.withOpTimeout(ctx)
	defer cancel()
	_, err := s.Client.DeleteObjectWithContext(opCtx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.PublicBucket),
		Key:    aws.String(key),
	})
	return err
}
