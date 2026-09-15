package storage

import (
	"bytes"
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
)

// newFakeS3Storage points an S3Storage at a test HTTP server speaking just
// enough of the S3 wire surface (PUT/DELETE status codes) for the SDK's
// PutObject/DeleteObject paths.
func newFakeS3Storage(t *testing.T, handler http.HandlerFunc) *S3Storage {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := config.NewConfig()
	cfg.Storage.StorageType = "s3"
	cfg.Storage.Region = "us-east-1"
	cfg.Storage.Endpoint = server.URL
	cfg.Storage.BucketName = "test-bucket"
	cfg.Storage.BasePath = "uploads"
	cfg.Storage.AccessKey = "test-access"
	cfg.Storage.SecretKey = "test-secret"
	cfg.Storage.PathStyle = true
	st, err := NewS3Storage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestS3NormalUploadSucceeds(t *testing.T) {
	var gotBody []byte
	st := newFakeS3Storage(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	})
	if err := st.Save(context.Background(), "users/1/x.txt", strings.NewReader("hello")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if string(gotBody) != "hello" {
		t.Fatalf("uploaded body = %q", string(gotBody))
	}
}

func TestS3CallerCancellationReachesSDK(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	st := newFakeS3Storage(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			return
		case <-release:
			w.WriteHeader(http.StatusOK)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond) // let the request reach the server
		cancel()
	}()
	go func() {
		errCh <- st.Save(ctx, "users/1/cancelled.txt", strings.NewReader("x"))
	}()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Save succeeded despite cancellation")
		}
		if awsErr, ok := err.(awserr.Error); !ok || awsErr.Code() != request.CanceledErrorCode {
			t.Fatalf("err = %v, want request-context cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Save ignored caller cancellation")
	}
}

func TestS3DeadlineExpiryBounded(t *testing.T) {
	st := newFakeS3Storage(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := st.Save(ctx, "users/1/slow.txt", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected deadline expiry error")
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("deadline not enforced: took %v", elapsed)
	}
}

func TestS3StalledEndpointCannotHangIndefinitely(t *testing.T) {
	// Server accepts the connection but never responds; only the storage
	// client's own op timeout can save the caller here.
	stall := make(chan struct{})
	st := newFakeS3Storage(t, func(w http.ResponseWriter, r *http.Request) {
		<-stall // block past any reasonable deadline
		w.WriteHeader(http.StatusOK)
	})
	// Cleanup order matters: close(stall) runs before the fake server's
	// Close (t.Cleanup is LIFO), unblocking the handler first.
	t.Cleanup(func() { close(stall) })
	st.opTimeout = 500 * time.Millisecond

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- st.Save(context.Background(), "users/1/stalled.txt", strings.NewReader("x"))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled endpoint returned success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("stalled endpoint hung the caller beyond op timeout")
	}
	if elapsed := time.Since(start); elapsed > 12*time.Second {
		t.Fatalf("op timeout not applied: %v", elapsed)
	}
}

func TestS3RetriesAreBounded(t *testing.T) {
	var attempts atomic.Int32
	st := newFakeS3Storage(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, `{"__type":"InternalError"}`)
	})
	start := time.Now()
	err := st.Save(context.Background(), "users/1/failing.txt", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected error from persistent 500")
	}
	// s3MaxRetries = 3 means at most 4 total attempts (initial + 3 retries).
	if got := attempts.Load(); got > 4 {
		t.Fatalf("attempts = %d, want <= 4 (bounded retries)", got)
	}
	if elapsed := time.Since(start); elapsed > DefaultS3OperationTimeout {
		t.Fatalf("retry loop exceeded op timeout: %v", elapsed)
	}
}

func TestS3ErrorDoesNotLeakCredentials(t *testing.T) {
	st := newFakeS3Storage(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintln(w, `{"__type":"AccessDenied","message":"nope"}`)
	})
	err := st.Save(context.Background(), "users/1/denied.txt", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected access-denied error")
	}
	msg := err.Error()
	for _, secret := range []string{"test-secret", "test-access"} {
		if bytes.Contains([]byte(msg), []byte(secret)) {
			t.Fatalf("error message leaks credential %q: %s", secret, msg)
		}
	}
}
