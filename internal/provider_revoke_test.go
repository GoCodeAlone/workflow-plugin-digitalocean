package internal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/digitalocean/godo"
)

// capturedRequestServer returns a DOProvider + a function that returns the last
// request received by the stub server. The last-request pointer is protected by
// a mutex to avoid a data race between the httptest server goroutine (writer)
// and the test goroutine (reader) when running under -race.
func capturedRequestServer(t *testing.T, statusCode int) (*DOProvider, func() *http.Request) {
	t.Helper()
	var mu sync.Mutex
	var last *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r
		mu.Unlock()
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(srv.Close)

	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	client.BaseURL = base
	return &DOProvider{client: client, region: "nyc3"}, func() *http.Request {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// TestDOProvider_RevokeProviderCredential_204 verifies that a 204 response is
// treated as successful revocation.
func TestDOProvider_RevokeProviderCredential_204(t *testing.T) {
	p, getReq := capturedRequestServer(t, http.StatusNoContent)

	err := p.RevokeProviderCredential(context.Background(), "digitalocean.spaces", "AKID123")
	if err != nil {
		t.Fatalf("RevokeProviderCredential: unexpected error: %v", err)
	}
	req := getReq()
	if req == nil {
		t.Fatal("no request captured by stub server")
	}
	if req.Method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", req.Method)
	}
	wantPath := "/v2/spaces/keys/AKID123"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
}

// jsonStubServer starts an httptest server that responds with the given status code and
// a minimal JSON error body, returning a configured DOProvider. The server is closed via
// t.Cleanup.
func jsonStubServer(t *testing.T, statusCode int, body string) *DOProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = fmt.Fprintln(w, body)
	}))
	t.Cleanup(srv.Close)

	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	client.BaseURL = base
	return &DOProvider{client: client, region: "nyc3"}
}

// TestDOProvider_RevokeProviderCredential_404 verifies that a 404 response is
// treated as success (key already absent — idempotent).
func TestDOProvider_RevokeProviderCredential_404(t *testing.T) {
	// godo expects an error body for 4xx/5xx responses to populate ErrorResponse.
	p := jsonStubServer(t, http.StatusNotFound, `{"id":"not_found","message":"key not found"}`)

	err := p.RevokeProviderCredential(context.Background(), "digitalocean.spaces", "AKID_GONE")
	if err != nil {
		t.Fatalf("RevokeProviderCredential with 404: expected nil error (idempotent), got: %v", err)
	}
}

// TestDOProvider_RevokeProviderCredential_401 verifies that a 401 response
// propagates as an error (auth failure should surface to operator).
func TestDOProvider_RevokeProviderCredential_401(t *testing.T) {
	p := jsonStubServer(t, http.StatusUnauthorized, `{"id":"unauthorized","message":"unable to authenticate"}`)

	err := p.RevokeProviderCredential(context.Background(), "digitalocean.spaces", "AKID_AUTH")
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

// TestDOProvider_RevokeProviderCredential_5xx verifies that a 5xx response
// propagates as an error (transient; caller logs + continues).
func TestDOProvider_RevokeProviderCredential_5xx(t *testing.T) {
	p := jsonStubServer(t, http.StatusInternalServerError, `{"id":"server_error","message":"internal server error"}`)

	err := p.RevokeProviderCredential(context.Background(), "digitalocean.spaces", "AKID_5XX")
	if err == nil {
		t.Fatal("expected error on 5xx, got nil")
	}
}

// TestDOProvider_RevokeProviderCredential_UnknownSource verifies that an
// unsupported source returns an error without making any HTTP request.
func TestDOProvider_RevokeProviderCredential_UnknownSource(t *testing.T) {
	p := &DOProvider{} // no client needed — error should be returned before HTTP call
	err := p.RevokeProviderCredential(context.Background(), "aws.s3", "AKID_AWS")
	if err == nil {
		t.Fatal("expected error for unknown source, got nil")
	}
}

// TestDOProvider_RevokeProviderCredential_EmptyCredentialID verifies that an
// empty credentialID returns an error without making any HTTP request.
func TestDOProvider_RevokeProviderCredential_EmptyCredentialID(t *testing.T) {
	p := &DOProvider{}
	err := p.RevokeProviderCredential(context.Background(), "digitalocean.spaces", "")
	if err == nil {
		t.Fatal("expected error for empty credentialID, got nil")
	}
}
