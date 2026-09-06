package gchat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a non-2xx answer from Google, parsed from the AIP-193
// error envelope. Body text is never included: a Chat error message can
// quote message content, and this error reaches logs.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Status is Google's canonical code, such as PERMISSION_DENIED.
	Status string
	// Message is Google's human-readable message.
	Message string
	// Reason is the typed reason from error.details[], such as
	// ACCESS_TOKEN_SCOPE_INSUFFICIENT. Not every endpoint sets it.
	Reason string
	// Scope is the OAuth scope the endpoint needed, taken from the
	// request rather than from Google's answer. It is what a refusal
	// for want of consent tells the person to grant.
	Scope string
	// AlsoScopes are further scopes the call needed, set only where an
	// argument widens what is read. Google's refusal does not say which
	// of them was declined, so all are named.
	AlsoScopes []string
	// Method is the request that failed, for the log line.
	Method string
	// Path is the request path, without query, for the log line.
	Path string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "chat api: %s %s: %d", e.Method, e.Path, e.StatusCode)
	if e.Status != "" {
		fmt.Fprintf(&b, " %s", e.Status)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	// The reason is what says whether a refusal can be acted on —
	// "insufficientPermissions" is a different problem from "you may not
	// touch this" and Google's message often reads the same for both. It
	// is parsed and classified but was never shown, so every 403 reached
	// a person looking identical.
	if e.Reason != "" && e.Reason != e.Status {
		fmt.Fprintf(&b, " (%s)", e.Reason)
	}
	return b.String()
}

// apiError parses a failed answer and stamps it with the scopes this
// call needs.
//
// The two halves belong together: Classify turns *APIError into the
// "grant this scope" message, and a transport path that parsed the
// error but forgot the scopes would produce a refusal naming nothing.
// There are three send paths now, and this is the one place that
// pairing lives.
func (r request) apiError(statusCode int, body []byte, path string) *APIError {
	e := parseAPIError(statusCode, body, r.method, path)
	e.Scope, e.AlsoScopes = r.scope, r.alsoScopes
	return e
}

// errorEnvelope is Google's error body.
type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type   string `json:"@type"`
			Reason string `json:"reason"`
		} `json:"details"`
		// The older frontend puts the reason here instead, and spells it
		// camelCase where an ErrorInfo detail spells it UPPER_SNAKE.
		// Reading only one of the two loses the reason entirely for
		// whichever shape the endpoint happens to send.
		Errors []struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error"`
}

// parseAPIError builds an APIError from a response body. A body that is
// not an error envelope still yields a usable error from the status.
func parseAPIError(statusCode int, body []byte, method, path string) *APIError {
	e := &APIError{StatusCode: statusCode, Method: method, Path: path}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil {
		e.Status = env.Error.Status
		e.Message = env.Error.Message
		for _, d := range env.Error.Details {
			if d.Reason != "" {
				e.Reason = d.Reason
				break
			}
		}
		for _, d := range env.Error.Errors {
			if e.Reason == "" && d.Reason != "" {
				e.Reason = d.Reason
			}
		}
	}
	if e.Status == "" {
		e.Status = http.StatusText(statusCode)
	}
	return e
}

// scopeReasons and scopeMessages are how Google says "you did not ask
// for this scope".
//
// Two spellings each, because not every endpoint fills in the typed
// reason and the older API frontend words the message differently. The
// list is deliberately generous: reading it wrongly as a missing scope
// costs a wasted login prompt, while missing it lets IsAlreadyGone
// report a refused delete as a deletion that already happened.
var (
	scopeReasons  = []string{"ACCESS_TOKEN_SCOPE_INSUFFICIENT", "insufficientPermissions"}
	scopeMessages = []string{"insufficient authentication scopes", "insufficient permission"}
)

// sameReason compares two reason codes across Google's two spellings of
// them. An ErrorInfo detail says ACCESS_TOKEN_SCOPE_INSUFFICIENT where
// the older frontend says insufficientPermissions, and the same
// condition can arrive either way depending on the endpoint. Comparing
// exactly means matching whichever spelling was written down and missing
// the other.
func sameReason(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(strings.ReplaceAll(a, "_", ""), strings.ReplaceAll(b, "_", ""))
}

// IsMissingScope reports whether err is Google refusing for want of an
// OAuth scope, rather than for want of permission.
func IsMissingScope(err error) bool {
	var e *APIError
	if !errors.As(err, &e) || e.StatusCode != http.StatusForbidden {
		return false
	}
	for _, want := range scopeReasons {
		if sameReason(e.Reason, want) {
			return true
		}
	}
	// No gate on Status. parseAPIError fills it with http.StatusText when
	// the envelope carries none, so a guard on "PERMISSION_DENIED" made
	// this unreachable for exactly the shape that needs it: the older
	// frontend sends no status field, and its 403 says "Insufficient
	// Permission" in the message. Being on a 403 at all is the gate.
	message := strings.ToLower(e.Message)
	for _, want := range scopeMessages {
		if strings.Contains(message, want) {
			return true
		}
	}
	return false
}

// IsNotFound reports whether err is a 404 or Google's NOT_FOUND.
func IsNotFound(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return e.StatusCode == http.StatusNotFound || e.Status == "NOT_FOUND"
}

// IsAlreadyExists reports whether err is Google rejecting a create whose
// resource is already there. send_message relies on it: a retry carrying
// the same message id must be recognised, not reported as a failure.
func IsAlreadyExists(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return e.StatusCode == http.StatusConflict || e.Status == "ALREADY_EXISTS"
}

// IsAlreadyGone reports whether a delete's target is already absent, so
// the call was a no-op rather than a failure.
//
// The first branch carries the weight. A missing-scope 403 must never
// read as "already gone", or the caller is told the delete succeeded
// when what they needed was a prompt to grant a scope.
//
// A plain refusal is not decided here. Google answers "you may not
// delete this" and "this is already deleted, and the space keeps no
// history" with the same 403, and the only way to tell them apart is to
// go and look; internal/service does that on the refusal path.
func IsAlreadyGone(err error) bool {
	if IsMissingScope(err) {
		return false
	}
	return IsNotFound(err)
}

// IsForbidden reports whether Google refused the call outright, as
// opposed to refusing it for want of a scope or a quota. Those two are
// also 403s and mean something a caller can act on differently.
func IsForbidden(err error) bool {
	if IsMissingScope(err) || IsRateLimited(err) || IsQuotaExceeded(err) {
		return false
	}
	var e *APIError
	return errors.As(err, &e) && e.StatusCode == http.StatusForbidden
}

// quotaReasons are how Google says a quota is spent rather than a rate
// exceeded, on a 403 rather than a 429. Told apart because the answer
// differs: a rate limit clears in seconds, a daily quota does not clear
// by waiting at all.
//
// The distinction came from the google-docs-mcp session, which keys its
// own map on the reason with a flag for "backing off can help".
var (
	quotaReasons = []string{"dailyLimitExceeded", "quotaExceeded"}
	rateReasons  = []string{"rateLimitExceeded", "userRateLimitExceeded"}
)

// IsRateLimited reports whether Google asked the caller to slow down.
//
// A 429 always means it. A 403 can also mean it: Google's classic
// error shape puts a rate limit on a 403 with a reason, which read as a
// plain refusal here until the reason was looked at.
func IsRateLimited(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	if e.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return e.StatusCode == http.StatusForbidden && matchesReason(e.Reason, rateReasons)
}

// IsQuotaExceeded reports whether a quota is spent. Waiting does not
// clear it on the timescale a retry cares about.
func IsQuotaExceeded(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return matchesReason(e.Reason, quotaReasons)
}

// matchesReason compares a reason against a list, across Google's two
// spellings of the same word.
func matchesReason(reason string, want []string) bool {
	for _, w := range want {
		if sameReason(reason, w) {
			return true
		}
	}
	return false
}

// IsUnauthorized reports whether the access token was rejected.
func IsUnauthorized(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.StatusCode == http.StatusUnauthorized
}

// retryable reports whether another attempt could succeed. Only the
// transport and the server's own failures qualify; a 4xx will not change
// its mind, apart from the 429 that asked for a wait.
func retryable(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

// turnedAway reports the answers that mean Google rejected the request
// rather than failed partway through applying it. It is what a write
// carrying no idempotency key may be retried on.
//
// 429 is a refusal to start. 503 is Google saying it is not serving
// this. Everything else in the 5xx range is ambiguous: a 500 can follow
// a commit, and a 502 or 504 comes from a gateway that never learned
// what the backend did.
func turnedAway(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable
}
