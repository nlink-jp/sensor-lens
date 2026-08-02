package switchbot

import (
	"errors"
	"fmt"
)

// SwitchBot statusCode values carried in the envelope of an otherwise HTTP-200
// response. Only the ones sensor-lens can actually meet are named.
const (
	StatusSuccess        = 100
	StatusDeviceType     = 151 // device type error
	StatusDeviceNotFound = 152
	StatusUnsupported    = 160 // command not supported
	StatusDeviceOffline  = 161
	StatusHubOffline     = 171
	StatusSystemError    = 190 // device state not synchronized with the server
)

// APIError is a failure reported by the SwitchBot API, either as an HTTP status
// or as a non-100 statusCode inside an HTTP-200 envelope.
type APIError struct {
	// StatusCode is the SwitchBot envelope code, or 0 when the failure was
	// purely at the HTTP layer.
	StatusCode int
	// HTTPStatus is the HTTP response code.
	HTTPStatus int
	Message    string
}

func (e *APIError) Error() string {
	switch {
	case e.StatusCode != 0 && e.Message != "":
		return fmt.Sprintf("switchbot: statusCode %d: %s", e.StatusCode, e.Message)
	case e.StatusCode != 0:
		return fmt.Sprintf("switchbot: statusCode %d", e.StatusCode)
	case e.Message != "":
		return fmt.Sprintf("switchbot: HTTP %d: %s", e.HTTPStatus, e.Message)
	default:
		return fmt.Sprintf("switchbot: HTTP %d", e.HTTPStatus)
	}
}

// Transient reports whether retrying later could plausibly succeed without any
// change of configuration.
//
// A device or its hub being offline is the common case: the reading is simply
// unavailable right now. Treating it as fatal would take the daemon down every
// time a meter drops off the mesh, so the poller records nothing for that
// device and carries on.
func (e *APIError) Transient() bool {
	switch e.StatusCode {
	case StatusDeviceOffline, StatusHubOffline, StatusSystemError:
		return true
	}
	return e.HTTPStatus == 429 || e.HTTPStatus >= 500
}

// RateLimited reports whether the failure looks like the daily call quota.
//
// The API does not distinguish an exhausted quota from a bad token: going over
// 10,000 calls a day starts answering "Unauthorized", the same HTTP 401 an
// invalid token produces. Callers must therefore treat a 401 as *possibly*
// quota-related and lean on the locally tracked call count to tell the two
// apart — which is exactly why sensor-lens keeps that count itself.
func (e *APIError) RateLimited() bool {
	return e.HTTPStatus == 429 || e.HTTPStatus == 401
}

// AsAPIError extracts an *APIError from an error chain.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	ok := errors.As(err, &apiErr)
	return apiErr, ok
}
