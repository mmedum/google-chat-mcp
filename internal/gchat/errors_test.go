package gchat

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func apiErr(status int, googleStatus, message, reason string) *APIError {
	return &APIError{
		StatusCode: status, Status: googleStatus, Message: message, Reason: reason,
		Method: "GET", Path: "spaces/AAA",
	}
}

func TestParseAPIErrorReadsTheEnvelope(t *testing.T) {
	body := `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied",
	  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`
	e := parseAPIError(403, []byte(body), "GET", "spaces")
	if e.Status != "PERMISSION_DENIED" || e.Message != "denied" || e.Reason != "ACCESS_TOKEN_SCOPE_INSUFFICIENT" {
		t.Errorf("parsed = %+v", e)
	}
}

// A body that is not an error envelope still has to yield a usable
// error, or an HTML error page from a proxy becomes a nil dereference.
func TestParseAPIErrorSurvivesAnUnexpectedBody(t *testing.T) {
	e := parseAPIError(http.StatusBadGateway, []byte("<html>bad gateway</html>"), "GET", "spaces")
	if e.StatusCode != http.StatusBadGateway || e.Status != http.StatusText(http.StatusBadGateway) {
		t.Errorf("parsed = %+v", e)
	}
	if !strings.Contains(e.Error(), "502") {
		t.Errorf("message = %q", e.Error())
	}
}

// The error text reaches logs, so it must name the call without
// repeating whatever Google quoted back.
func TestErrorTextNamesTheCall(t *testing.T) {
	got := apiErr(404, "NOT_FOUND", "no such space", "").Error()
	for _, want := range []string{"GET", "spaces/AAA", "404", "NOT_FOUND"} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q does not mention %q", got, want)
		}
	}
}

func TestIsMissingScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"typed reason", apiErr(403, "PERMISSION_DENIED", "denied", "ACCESS_TOKEN_SCOPE_INSUFFICIENT"), true},
		{"message fallback", apiErr(403, "PERMISSION_DENIED", "Request had insufficient authentication scopes.", ""), true},
		{"message fallback, other case", apiErr(403, "PERMISSION_DENIED", "INSUFFICIENT AUTHENTICATION SCOPES", ""), true},
		// The two spellings of one reason, either of which an endpoint
		// may send: the older frontend's camelCase and the ErrorInfo
		// upper snake case of the same words. Raised by the
		// google-docs-mcp session, which matches reasons exactly and
		// would see only the spelling it wrote down.
		{"camelCase reason", apiErr(403, "PERMISSION_DENIED", "denied", "insufficientPermissions"), true},
		{"the same reason in upper snake case", apiErr(403, "PERMISSION_DENIED", "denied", "INSUFFICIENT_PERMISSIONS"), true},
		{"plain permission denied", apiErr(403, "PERMISSION_DENIED", "you are not a member", ""), false},
		{"not a 403", apiErr(404, "NOT_FOUND", "gone", ""), false},
		{"reason on the wrong status", apiErr(400, "INVALID_ARGUMENT", "x", "ACCESS_TOKEN_SCOPE_INSUFFICIENT"), false},
		{"not an api error", errors.New("connection reset"), false},
		{"nil", nil, false},
		{"wrapped", fmt.Errorf("calling google: %w", apiErr(403, "PERMISSION_DENIED", "x", "ACCESS_TOKEN_SCOPE_INSUFFICIENT")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMissingScope(tc.err); got != tc.want {
				t.Errorf("IsMissingScope = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsNotFoundAndIsAlreadyExists(t *testing.T) {
	if !IsNotFound(apiErr(404, "", "", "")) || !IsNotFound(apiErr(400, "NOT_FOUND", "", "")) {
		t.Error("a 404 or a NOT_FOUND status is not found")
	}
	if IsNotFound(apiErr(403, "PERMISSION_DENIED", "", "")) || IsNotFound(errors.New("x")) {
		t.Error("false positive on not found")
	}
	if !IsAlreadyExists(apiErr(409, "", "", "")) || !IsAlreadyExists(apiErr(400, "ALREADY_EXISTS", "", "")) {
		t.Error("a 409 or ALREADY_EXISTS is already-exists")
	}
	if IsAlreadyExists(apiErr(404, "NOT_FOUND", "", "")) {
		t.Error("false positive on already exists")
	}
}

func TestIsRateLimitedAndIsUnauthorized(t *testing.T) {
	if !IsRateLimited(apiErr(429, "RESOURCE_EXHAUSTED", "", "")) || IsRateLimited(apiErr(500, "", "", "")) {
		t.Error("rate limit detection is wrong")
	}
	if !IsUnauthorized(apiErr(401, "UNAUTHENTICATED", "", "")) || IsUnauthorized(apiErr(403, "", "", "")) {
		t.Error("unauthorized detection is wrong")
	}
}

// The load-bearing rule: a missing-scope 403 must never read as "already
// gone", or a delete reports success when the caller needed a prompt to
// grant a scope.
func TestIsAlreadyGoneNeverSwallowsAMissingScope(t *testing.T) {
	scopeErr := apiErr(403, "PERMISSION_DENIED", "Request had insufficient authentication scopes.", "")
	if IsAlreadyGone(scopeErr) {
		t.Error("a missing-scope 403 read as already gone")
	}
	if IsForbidden(scopeErr) {
		t.Error("a missing-scope 403 read as a plain refusal, which would send it down the confirm path")
	}
}

func TestIsAlreadyGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a 404 is gone", apiErr(404, "NOT_FOUND", "", ""), true},
		// A plain 403 is not decided here: Google spells "already
		// deleted, history off" and "you may not delete this" the same
		// way, and internal/service goes and looks.
		{"a plain 403 is not gone on its own", apiErr(403, "PERMISSION_DENIED", "no access", ""), false},
		{"a 500 is never gone", apiErr(500, "INTERNAL", "", ""), false},
		{"a transport error is never gone", errors.New("connection reset"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAlreadyGone(tc.err); got != tc.want {
				t.Errorf("IsAlreadyGone = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsForbidden(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a plain refusal", apiErr(403, "PERMISSION_DENIED", "no access", ""), true},
		{"a missing scope is not a plain refusal", apiErr(403, "PERMISSION_DENIED", "Insufficient Permission", ""), false},
		{"a 404 is not a refusal", apiErr(404, "NOT_FOUND", "", ""), false},
		{"a transport error is not a refusal", errors.New("connection reset"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsForbidden(tc.err); got != tc.want {
				t.Errorf("IsForbidden = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRetryable(t *testing.T) {
	for status, want := range map[int]bool{
		429: true, 500: true, 502: true, 503: true, 504: true,
		400: false, 401: false, 403: false, 404: false, 409: false, 200: false,
	} {
		if got := retryable(status); got != want {
			t.Errorf("retryable(%d) = %v, want %v", status, got, want)
		}
	}
}

// Google words a scope refusal two ways: the typed reason on the newer
// API frontend, and a plainer message on the older one. Reading either
// one wrongly as a permission refusal would let IsAlreadyGone report a
// delete that never happened.
func TestIsMissingScopeKnowsBothOfGooglesSpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"typed reason", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied",
		  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`, true},
		{"older typed reason", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied",
		  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"insufficientPermissions"}]}}`, true},
		{"newer message", `{"error":{"code":403,"status":"PERMISSION_DENIED",
		  "message":"Request had insufficient authentication scopes."}}`, true},
		{"older message", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Insufficient Permission"}}`, true},
		{"a plain refusal", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"You are not a member"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := parseAPIError(403, []byte(tc.body), "DELETE", "spaces/A/messages/B")
			if got := IsMissingScope(err); got != tc.want {
				t.Errorf("IsMissingScope = %v, want %v", got, tc.want)
			}
			// The one that matters: a scope refusal is never "already
			// gone", or a caller is told a delete succeeded when what
			// they needed was a prompt to grant a scope.
			if tc.want && IsAlreadyGone(err) {
				t.Error("a missing scope was reported as an already-deleted resource")
			}
		})
	}
}

// Google's older frontend sends a 403 with no `status`, the reason under
// `errors[]` rather than `details[]`, and camelCase where an ErrorInfo
// detail is UPPER_SNAKE. That shape used to defeat all three checks at
// once: the reason was never parsed, the spelling would not have matched
// if it had been, and the defaulted Status of "Forbidden" stopped the
// message fallback from ever running. A missed scope error is reported
// as a plain refusal, so the person is never told to grant and re-login.
func TestTheLegacyForbiddenShapeIsRecognised(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			"older frontend: no status, reason under errors[], camelCase",
			`{"error":{"code":403,"message":"Insufficient Permission",
			  "errors":[{"message":"Insufficient Permission","reason":"insufficientPermissions"}]}}`,
		},
		{
			"ErrorInfo detail, UPPER_SNAKE",
			`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes.",
			  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`,
		},
		{
			"the same condition spelled the other way round in a detail",
			`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"nothing useful",
			  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_PERMISSIONS"}]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := parseAPIError(http.StatusForbidden, []byte(tc.body), "DELETE", "spaces/AAAAspace1/messages/AAAAmsg1")
			if !IsMissingScope(err) {
				t.Errorf("not recognised as a missing scope: %v", err)
			}
			if IsAlreadyGone(err) {
				t.Error("a missing scope must never read as already gone")
			}
		})
	}
}

// A genuine refusal must still be one, or every "you may not do this"
// turns into a pointless re-login prompt.
func TestAPlainRefusalIsNotAScopeError(t *testing.T) {
	body := `{"error":{"code":403,"status":"PERMISSION_DENIED",
	  "message":"Permission denied to perform the requested action on the specified resource."}}`
	err := parseAPIError(http.StatusForbidden, []byte(body), "PATCH", "spaces/AAAAspace1/messages/AAAAmsg1")
	if IsMissingScope(err) {
		t.Errorf("a plain refusal read as a missing scope: %v", err)
	}
	if !IsForbidden(err) {
		t.Error("a plain refusal should be reported as forbidden")
	}
}
