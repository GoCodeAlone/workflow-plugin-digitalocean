package drivers

import (
	"fmt"
	"net/http"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// WrapGodoError inspects err for a *godo.ErrorResponse and maps its HTTP status
// code to the matching interfaces.Err* sentinel. The returned error wraps the
// sentinel so callers can use errors.Is for classification while still having
// access to the original DO API message via err.Error().
//
// If err is nil or not a *godo.ErrorResponse, it is returned unchanged.
func WrapGodoError(err error) error {
	if err == nil {
		return nil
	}
	gErr, ok := err.(*godo.ErrorResponse)
	if !ok || gErr.Response == nil {
		return err
	}
	sentinel := sentinelForStatus(gErr.Response.StatusCode)
	if sentinel == nil {
		return err
	}
	return fmt.Errorf("%w: %v", sentinel, err)
}

// sentinelForStatus maps an HTTP status code to its interfaces sentinel.
// Returns nil for codes that have no sentinel mapping (pass-through).
func sentinelForStatus(code int) error {
	switch {
	case code == http.StatusUnauthorized:
		return interfaces.ErrUnauthorized
	case code == http.StatusForbidden:
		return interfaces.ErrForbidden
	case code == http.StatusNotFound || code == http.StatusMethodNotAllowed:
		return interfaces.ErrResourceNotFound
	case code == http.StatusConflict:
		return interfaces.ErrResourceAlreadyExists
	case code == http.StatusBadRequest || code == http.StatusUnprocessableEntity:
		return interfaces.ErrValidation
	case code == http.StatusTooManyRequests:
		return interfaces.ErrRateLimited
	case code >= 500 && code <= 599:
		return interfaces.ErrTransient
	default:
		return nil
	}
}
