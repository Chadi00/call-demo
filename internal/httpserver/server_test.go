package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chadiek/call-demo/internal/config"
)

func TestServer_Healthz(t *testing.T) {
	srv := New(config.Config{})
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRtcAuthOK(t *testing.T) {
	// Missing expected -> accept
	if !rtcAuthOK(nil, "") {
		t.Fatalf("expected true when expected empty")
	}
	r := httptest.NewRequest(http.MethodGet, "/?password=secret", nil)
	if !rtcAuthOK(r, "secret") {
		t.Fatalf("expected true with query password")
	}
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set("X-Auth-Token", "tok")
	if !rtcAuthOK(r2, "tok") {
		t.Fatalf("expected true with X-Auth-Token")
	}
	r3 := httptest.NewRequest(http.MethodGet, "/", nil)
	r3.Header.Set("Authorization", "Bearer abc")
	if !rtcAuthOK(r3, "abc") {
		t.Fatalf("expected true with Authorization bearer")
	}
}

func TestRtcAuthOK_BearerCaseInsensitivePrefix(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "bearer abc")
	if !rtcAuthOK(r, "abc") {
		t.Fatalf("expected true with lowercase bearer prefix")
	}
}

func TestRtcAuthOK_NegativeCases(t *testing.T) {
	// Wrong query token
	r1 := httptest.NewRequest(http.MethodGet, "/?password=wrong", nil)
	if rtcAuthOK(r1, "secret") {
		t.Fatalf("expected false with wrong query token")
	}
	// Wrong X-Auth-Token header
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set("X-Auth-Token", "nope")
	if rtcAuthOK(r2, "secret") {
		t.Fatalf("expected false with wrong X-Auth-Token")
	}
	// Wrong Bearer token
	r3 := httptest.NewRequest(http.MethodGet, "/", nil)
	r3.Header.Set("Authorization", "Bearer nope")
	if rtcAuthOK(r3, "secret") {
		t.Fatalf("expected false with wrong bearer token")
	}
}

func TestCall_MethodNotAllowed(t *testing.T) {
	srv := New(config.Config{})
	r := httptest.NewRequest(http.MethodGet, "/call", nil)
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestCall_BadJSON(t *testing.T) {
	srv := New(config.Config{})
	r := httptest.NewRequest(http.MethodPost, "/call", strings.NewReader("not-json"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCall_Unauthorized(t *testing.T) {
	srv := New(config.Config{AuthPassword: "secret"})
	// No token provided
	r := httptest.NewRequest(http.MethodPost, "/call", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	// Wrong token provided
	r2 := httptest.NewRequest(http.MethodPost, "/call?password=wrong", strings.NewReader("{}"))
	r2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	srv.Router.ServeHTTP(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w2.Code)
	}
}
