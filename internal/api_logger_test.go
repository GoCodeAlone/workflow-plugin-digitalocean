package internal

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingRoundTripperSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	lrt := newLoggingRoundTripper(http.DefaultTransport)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := lrt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestLoggingRoundTripperErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"id":"not_found","message":"Image tag or digest not found"}`))
	}))
	defer srv.Close()

	lrt := newLoggingRoundTripper(http.DefaultTransport)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"spec":{"name":"test-app"}}`))
	resp, err := lrt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	// body must still be readable after logging
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not_found") {
		t.Fatalf("response body not readable after logging: %q", body)
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("a", 600)
	result := truncate(long, debugAPIBodyLimit)
	if len(result) > debugAPIBodyLimit+5 {
		t.Fatalf("truncate returned too long string: len=%d", len(result))
	}
	if !strings.HasSuffix(result, "…") {
		t.Fatalf("expected ellipsis suffix, got: %s", result[len(result)-3:])
	}
}
