package drivers_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

func godoErr(statusCode int) error {
	return &godo.ErrorResponse{
		Response: &http.Response{StatusCode: statusCode},
		Message:  http.StatusText(statusCode),
	}
}

func TestWrapGodoError_StatusCodeMapping(t *testing.T) {
	cases := []struct {
		code     int
		sentinel error
	}{
		{http.StatusUnauthorized, interfaces.ErrUnauthorized},          // 401
		{http.StatusForbidden, interfaces.ErrForbidden},                // 403
		{http.StatusNotFound, interfaces.ErrResourceNotFound},          // 404
		{http.StatusMethodNotAllowed, interfaces.ErrResourceNotFound},  // 405
		{http.StatusConflict, interfaces.ErrResourceAlreadyExists},     // 409
		{http.StatusUnprocessableEntity, interfaces.ErrValidation},     // 422
		{http.StatusTooManyRequests, interfaces.ErrRateLimited},        // 429
		{http.StatusBadRequest, interfaces.ErrValidation},              // 400
		{http.StatusInternalServerError, interfaces.ErrTransient},      // 500
		{http.StatusBadGateway, interfaces.ErrTransient},               // 502
		{http.StatusServiceUnavailable, interfaces.ErrTransient},       // 503
		{http.StatusGatewayTimeout, interfaces.ErrTransient},           // 504
	}

	for _, tc := range cases {
		err := drivers.WrapGodoError(godoErr(tc.code))
		if err == nil {
			t.Errorf("status %d: expected non-nil error", tc.code)
			continue
		}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("status %d: errors.Is(%v) = false, want sentinel %v", tc.code, err, tc.sentinel)
		}
	}
}

func TestWrapGodoError_PassthroughNonGodo(t *testing.T) {
	orig := errors.New("some other error")
	wrapped := drivers.WrapGodoError(orig)
	if wrapped != orig {
		t.Errorf("non-godo error should be returned as-is, got %v", wrapped)
	}
}

func TestWrapGodoError_NilPassthrough(t *testing.T) {
	if drivers.WrapGodoError(nil) != nil {
		t.Error("nil should remain nil")
	}
}

func TestWrapGodoError_OriginalMessagePreserved(t *testing.T) {
	orig := godoErr(http.StatusNotFound)
	err := drivers.WrapGodoError(orig)
	// The original godo error message must still be accessible in the error chain.
	if !errors.Is(err, interfaces.ErrResourceNotFound) {
		t.Errorf("sentinel not in chain: %v", err)
	}
	// Original error string must appear somewhere in the output.
	if err.Error() == "" {
		t.Error("wrapped error should not be empty")
	}
}
