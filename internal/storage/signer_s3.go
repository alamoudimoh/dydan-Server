package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

// URLSignerS3 returns fetchable URLs for objects in the S3 bucket.
//
// In the default (signed) mode it returns SigV4 presigned URLs with a
// VALID_FOR lifetime. When the storage is configured with
// `public_read: true` the signer returns unsigned URLs instead — the
// caller is responsible for making the bucket anonymously readable.

type URLSignerS3 struct {
	storage    *S3Storage
	PublicHost string
	PublicRead bool
}

func NewURLSignerS3(storage *S3Storage, config *config.Config) *URLSignerS3 {
	return &URLSignerS3{
		storage:    storage,
		PublicHost: config.Storage.PublicHost,
		PublicRead: config.Storage.PublicRead,
	}
}

func (s *URLSignerS3) Sign(rawPath string) (string, error) {
	key := fmt.Sprintf("%s/%s", s.storage.BasePath, rawPath)
	req, _ := s.storage.Client.GetObjectRequest(&s3.GetObjectInput{
		Bucket: aws.String(s.storage.Bucket),
		Key:    aws.String(key),
	})

	var urlStr string
	if s.PublicRead {
		// Build the request URL without running the signing handlers so the
		// result has no X-Amz-* query params. The bucket is expected to be
		// configured for anonymous GETs.
		req.Build()
		if req.Error != nil {
			return "", req.Error
		}
		urlStr = req.HTTPRequest.URL.String()
	} else {
		presigned, err := req.Presign(VALID_FOR)
		if err != nil {
			return "", err
		}
		urlStr = presigned
	}

	if s.PublicHost != "" {
		parsedURL, err := url.Parse(urlStr)
		if err != nil {
			return "", err
		}
		parsedURL.Host = s.PublicHost
		urlStr = parsedURL.String()
	}

	return urlStr, nil
}

func (s *URLSignerS3) IsValid(rawPath string, query url.Values) bool {
	// In public-read mode the whole bucket is anonymously readable by
	// configuration, so the proxy route imposes no additional restriction.
	if s.PublicRead {
		return true
	}

	signature := query.Get("X-Amz-Signature")
	if signature == "" {
		// The serving route is unauthenticated; without a verifiable presigned
		// request every protected object would be world-readable. Fail closed.
		return false
	}

	if err := s.verifyPresigned(rawPath, query, signature); err != nil {
		return false
	}
	return true
}

// verifyPresigned re-computes the SigV4 presigned-URL signature locally.
// When storage is backed by S3 the proxy route fetches objects with the
// server's own credentials, so AWS never sees (or rejects) an invalid or
// expired presignature — validation has to happen here.
func (s *URLSignerS3) verifyPresigned(rawPath string, query url.Values, signature string) error {
	if query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		return fmt.Errorf("unsupported algorithm")
	}
	credential := query.Get("X-Amz-Credential")
	amzDate := query.Get("X-Amz-Date")
	expiresStr := query.Get("X-Amz-Expires")
	signedHeaders := query.Get("X-Amz-SignedHeaders")
	if credential == "" || amzDate == "" || expiresStr == "" || signedHeaders == "" {
		return fmt.Errorf("missing SigV4 parameter")
	}
	// Only host-signed presigned URLs can be validated here; we do not have
	// access to other headers on this code path.
	if signedHeaders != "host" {
		return fmt.Errorf("unsupported signed headers")
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil || expires <= 0 || expires > int64(VALID_FOR/time.Second) {
		return fmt.Errorf("invalid expiry")
	}
	signedAt, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return fmt.Errorf("invalid date")
	}
	if signedAt.Add(time.Duration(expires) * time.Second).Before(time.Now().UTC()) {
		return fmt.Errorf("presigned URL expired")
	}

	scopeParts := strings.Split(credential, "/")
	if len(scopeParts) != 5 || scopeParts[3] != "s3" || scopeParts[4] != "aws4_request" {
		return fmt.Errorf("invalid credential scope")
	}
	accessKey, dateScope, region := scopeParts[0], scopeParts[1], scopeParts[2]

	creds, err := s.storage.Client.Config.Credentials.Get()
	if err != nil {
		return fmt.Errorf("credentials unavailable")
	}
	if creds.AccessKeyID != accessKey {
		return fmt.Errorf("unknown access key")
	}

	canonicalQuery := canonicalSigV4Query(query)
	canonicalURI := "/" + escapedS3Key(s.objectKey(rawPath))

	var lastErr error
	for _, host := range s.candidateHosts(region) {
		canonicalRequest := strings.Join([]string{
			"GET",
			canonicalURI,
			canonicalQuery,
			"host:" + host + "\n",
			signedHeaders,
			"UNSIGNED-PAYLOAD",
		}, "\n")

		stringToSign := strings.Join([]string{
			"AWS4-HMAC-SHA256",
			amzDate,
			strings.Join([]string{dateScope, region, "s3", "aws4_request"}, "/"),
			hex.EncodeToString(hashSHA256([]byte(canonicalRequest))),
		}, "\n")

		key := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), dateScope)
		key = hmacSHA256(key, region)
		key = hmacSHA256(key, "s3")
		key = hmacSHA256(key, "aws4_request")
		expected := hex.EncodeToString(hmacSHA256(key, stringToSign))

		if hmac.Equal([]byte(expected), []byte(signature)) {
			return nil
		}
		lastErr = fmt.Errorf("signature mismatch for host %q", host)
	}
	return lastErr
}

// objectKey returns the bucket key that Sign() derived from rawPath.
func (s *URLSignerS3) objectKey(rawPath string) string {
	return fmt.Sprintf("%s/%s", s.storage.BasePath, rawPath)
}

// candidateHosts lists the Host values a presignature could have been
// computed against: path-style and virtual-hosted forms of the configured
// endpoint, plus the regional/global S3 endpoints. Presigned URLs are served
// by the bucket directly or through PublicHost proxies, so verification tries
// each form rather than assuming one.
func (s *URLSignerS3) candidateHosts(region string) []string {
	seen := map[string]bool{}
	candidates := []string{}
	add := func(host string) {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" && !seen[host] {
			seen[host] = true
			candidates = append(candidates, host)
		}
	}
	if endpoint := aws.StringValue(s.storage.Client.Config.Endpoint); endpoint != "" {
		if parsed, err := url.Parse(endpoint); err == nil {
			add(parsed.Host)
			add(s.storage.Bucket + "." + parsed.Host)
		}
	}
	if region != "" {
		add(fmt.Sprintf("%s.s3.%s.amazonaws.com", s.storage.Bucket, region))
		add(fmt.Sprintf("%s.s3.amazonaws.com", s.storage.Bucket))
	}
	return candidates
}

// canonicalSigV4Query builds the sorted, RFC3986-encoded query string with the
// signature itself excluded, per the SigV4 presigned URL spec.
func canonicalSigV4Query(query url.Values) string {
	type kv struct{ k, v string }
	pairs := make([]kv, 0, len(query))
	for k, vs := range query {
		if k == "X-Amz-Signature" {
			continue
		}
		for _, v := range vs {
			pairs = append(pairs, kv{sigV4Escape(k), sigV4Escape(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p.k+"="+p.v)
	}
	return strings.Join(parts, "&")
}

func sigV4Escape(s string) string {
	escaped := url.QueryEscape(s)
	// QueryEscape encodes spaces as '+' and '~' as %7E; SigV4 requires %20 and literal '~'.
	return strings.ReplaceAll(strings.ReplaceAll(escaped, "+", "%20"), "%7E", "~")
}

func escapedS3Key(key string) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = sigV4Escape(seg)
	}
	return strings.Join(segments, "/")
}

func hashSHA256(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// SignIfLocal returns a fetchable URL for a locally-stored path (by calling
// Sign) and passes through external URLs (e.g. from an OIDC `picture` claim
// or legacy rows that stored a full URL) unchanged. Use this wherever a
// user.Image field is being serialized back to a client so handlers that
// persist raw storage paths produce URLs browsers can actually fetch.
func (s *URLSignerS3) SignIfLocal(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	signed, err := s.Sign(path)
	if err != nil {
		return ""
	}
	return signed
}

func (s *URLSignerS3) SignAndGetPublicURL(rawPath string) (string, error) {
	if s.PublicRead {
		// If the bucket is public, return the public URL.
		return s.storage.GetPublicURL(nil, rawPath)
	}
	// Otherwise, return a signed URL.
	return s.Sign(rawPath)
}
