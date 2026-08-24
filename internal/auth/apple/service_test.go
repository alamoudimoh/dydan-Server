package apple

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alamoudimoh/daydan-server/config"
)

func TestPublicKeyCacheConcurrentRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"test","n":"Aw","e":"Aw"}]}`))
	}))
	defer server.Close()

	service := NewAppleService(&config.Config{})
	service.keysURL = server.URL
	service.httpClient = server.Client()

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.getPublicKey(context.Background(), "test")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent key lookup: %v", err)
		}
	}
}
