package schwab

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinel errors for Schwab responses, so callers can branch on meaning rather
// than on status codes. Ported from the SaaS backend's SchwabResponseHandler.
var (
	// ErrUnauthorized means the access token is missing, expired or rejected.
	// The remedy is a refresh, then a retry.
	ErrUnauthorized = errors.New("schwab rejected the access token")

	// ErrForbidden means the token is valid but lacks access to this resource,
	// usually an account the app was not granted.
	ErrForbidden = errors.New("schwab denied access to this resource")

	// ErrNotFound means the account hash or endpoint does not exist.
	ErrNotFound = errors.New("schwab resource not found")

	// ErrRateLimited means we are being throttled and should back off.
	ErrRateLimited = errors.New("schwab rate limit exceeded")

	// ErrSchwabUnavailable covers 5xx: Schwab's problem, not ours, and safe to
	// retry later. Never treat it as a credential failure.
	ErrSchwabUnavailable = errors.New("schwab is unavailable")
)

// APIError is a non-success response from Schwab. It wraps a sentinel so
// callers can use errors.Is, while retaining the status and body for logs.
type APIError struct {
	Op         string
	StatusCode int
	Body       string
	Err        error
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "schwab %s failed: HTTP %d", e.Op, e.StatusCode)
	if e.Err != nil {
		fmt.Fprintf(&b, " (%s)", e.Err)
	}
	if e.Body != "" {
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	return b.String()
}

func (e *APIError) Unwrap() error { return e.Err }

// classify turns a non-success response into an APIError wrapping the matching
// sentinel. A 400 gets no sentinel: on the token endpoint it means a dead
// grant, on a data endpoint it means a malformed request, and conflating those
// would send callers down the wrong recovery path.
func classify(op string, status int, body []byte) error {
	var sentinel error
	switch {
	case status == http.StatusUnauthorized:
		sentinel = ErrUnauthorized
	case status == http.StatusForbidden:
		sentinel = ErrForbidden
	case status == http.StatusNotFound:
		sentinel = ErrNotFound
	case status == http.StatusTooManyRequests:
		sentinel = ErrRateLimited
	case status >= 500:
		sentinel = ErrSchwabUnavailable
	}

	return &APIError{
		Op:         op,
		StatusCode: status,
		Body:       trimBody(body),
		Err:        sentinel,
	}
}

// trimBody collapses whitespace and caps length, so an HTML error page from a
// proxy does not flood a log line.
func trimBody(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	const max = 300
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
