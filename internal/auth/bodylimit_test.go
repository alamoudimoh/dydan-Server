package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLimitRequestBodyRejectsOversizedAndPassesNormal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LimitRequestBody(MaxAuthRequestBodyBytes))
	var gotLen int
	var readErr error
	r.POST("/login", func(c *gin.Context) {
		buf := make([]byte, MaxAuthRequestBodyBytes+16)
		n, err := c.Request.Body.Read(buf)
		gotLen, readErr = n, err
		c.Status(http.StatusOK)
	})

	// Normal credential payload passes untouched.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"u","password":"p"}`)))
	if w.Code != http.StatusOK || gotLen != len(`{"username":"u","password":"p"}`) || readErr != nil {
		t.Fatalf("normal body must pass intact: code=%d n=%d err=%v", w.Code, gotLen, readErr)
	}

	// Oversized body is cut off; handler sees a MaxBytesError.
	w = httptest.NewRecorder()
	huge := strings.Repeat("a", MaxAuthRequestBodyBytes+1024)
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(huge)))
	if !IsBodyTooLarge(readErr) && w.Code == http.StatusOK {
		t.Fatalf("oversized body must be rejected by MaxBytesReader, got %d", w.Code)
	}
	if IsBodyTooLarge(nil) {
		t.Fatal("nil must not be classified as too large")
	}
	if IsBodyTooLarge(errors.New("other")) {
		t.Fatal("unrelated error must not be classified as too large")
	}
}

func TestLimitRequestBodyNilBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LimitRequestBody(64))
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("nil-body request must pass: %d", w.Code)
	}
}
