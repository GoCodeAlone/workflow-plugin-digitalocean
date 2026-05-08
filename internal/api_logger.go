package internal

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	// debugAPIBodyLimit caps the number of bytes logged from request/response
	// bodies to keep output scannable even for large payloads.
	debugAPIBodyLimit = 500

	// EnvDebugAPI is the environment variable that enables API call logging.
	// Set to any non-empty value (e.g. WFCTL_DEBUG_API=1) to activate.
	EnvDebugAPI = "WFCTL_DEBUG_API"
)

// debugAPIEnabled returns true when WFCTL_DEBUG_API is set to a non-empty value.
func debugAPIEnabled() bool {
	return os.Getenv(EnvDebugAPI) != ""
}

// loggingRoundTripper wraps an http.RoundTripper and logs every request
// and response to stderr. Request/response bodies are captured,
// re-injected, and — for error responses (4xx/5xx) — included in the log
// output truncated to debugAPIBodyLimit bytes.
//
// When WFCTL_DEBUG_API is set, the godo client's HTTP transport is wrapped
// with this type so that every DigitalOcean API call is visible in the
// operator's terminal. This is intentionally low-level: it shows the raw
// HTTP method, URL, status code, and body excerpts, which is the most
// actionable information when diagnosing unexpected DO API failures (e.g.
// "404 Image tag or digest not found" from POST /v2/apps).
type loggingRoundTripper struct {
	base http.RoundTripper
}

// newLoggingRoundTripper wraps base with request/response logging.
// If base is nil, http.DefaultTransport is used.
func newLoggingRoundTripper(base http.RoundTripper) *loggingRoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &loggingRoundTripper{base: base}
}

// RoundTrip logs the outbound request and inbound response, then returns.
// Errors from the underlying transport are logged and returned unmodified.
func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqExcerpt string
	if req.Body != nil && req.Body != http.NoBody {
		body, err := io.ReadAll(req.Body)
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			reqExcerpt = truncate(string(body), debugAPIBodyLimit)
		}
	}

	fmt.Fprintf(os.Stderr, "[WFCTL_DEBUG_API] → %s %s\n", req.Method, req.URL)
	if reqExcerpt != "" {
		fmt.Fprintf(os.Stderr, "[WFCTL_DEBUG_API]   req body: %s\n", reqExcerpt)
	}

	resp, err := l.base.RoundTrip(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WFCTL_DEBUG_API] ✗ transport error: %v\n", err)
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "[WFCTL_DEBUG_API] ← %d %s\n", resp.StatusCode, req.URL)

	// For error responses, capture and log the body excerpt.
	if resp.StatusCode >= 400 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			fmt.Fprintf(os.Stderr, "[WFCTL_DEBUG_API]   resp body: %s\n", truncate(string(body), debugAPIBodyLimit))
		}
	}

	return resp, nil
}

// truncate returns s truncated to n bytes. If s is longer than n, an
// ellipsis suffix is appended to indicate truncation.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
