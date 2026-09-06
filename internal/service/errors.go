// Package service holds the rules: what a tool means, as opposed to how
// it is registered or how the wire looks. Nothing here imports MCP.
package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/auth"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// Class is the leading tag on a tool error. The model reads these, so
// they say what to do next rather than what went wrong internally.
type Class string

// Error classes.
const (
	// ClassAuth means no usable credentials. The person runs login.
	ClassAuth Class = "auth"
	// ClassScope means the token lacks an OAuth scope. The message
	// names the scope.
	ClassScope Class = "scope"
	// ClassNotFound means the space, message or person is not there.
	ClassNotFound Class = "not_found"
	// ClassInvalid means the arguments were wrong. The caller can fix
	// them and try again.
	ClassInvalid Class = "invalid"
	// ClassRateLimit means Google asked for a pause. Waiting helps.
	ClassRateLimit Class = "rate_limit"
	// ClassQuota means a quota is spent rather than a rate exceeded.
	// Waiting a moment does not help; waiting until tomorrow might.
	ClassQuota Class = "quota"
	// ClassForbidden means Google understood and refused. The scopes
	// are there; the permission is not. Retrying changes nothing, and
	// the answer is to ask a person for access.
	ClassForbidden Class = "forbidden"
	// ClassServer means Google failed rather than refused. Retrying is
	// reasonable; the request was not wrong.
	ClassServer Class = "server"
	// ClassUpstream is any other refusal from Google.
	ClassUpstream Class = "upstream"
	// ClassUnsupported means this server cannot do it at all: a file
	// transfer with no local directory configured, or bytes Chat does
	// not hold. No argument change helps, so the message says what
	// would.
	ClassUnsupported Class = "unsupported"
	// ClassUnexpected is a failure this server did not anticipate.
	ClassUnexpected Class = "unexpected"
)

// Error is a tool-facing failure. Its text is what the model sees.
type Error struct {
	Class Class
	// Message is written for the model: what happened and what to do.
	Message string
	// Scope is the OAuth scope that was missing, on ClassScope.
	Scope string
	err   error
}

func (e *Error) Error() string { return fmt.Sprintf("[%s] %s", e.Class, e.Message) }
func (e *Error) Unwrap() error { return e.err }

// Failf builds an Error.
func Failf(class Class, format string, args ...any) *Error {
	return &Error{Class: class, Message: fmt.Sprintf(format, args...)}
}

// Invalidf reports bad arguments.
func Invalidf(format string, args ...any) *Error {
	return Failf(ClassInvalid, format, args...)
}

// MissingScope builds the error a caller sees when Google refused for
// want of a scope.
//
// The scope URL is in the text on purpose. The person has to grant that
// exact string, and there is nowhere else to put it: the MCP spec has no
// structured payload on an error result, so the message is the channel.
//
// A call can need two, where an argument widens what it reads. Google's
// refusal does not say which one was declined, so both are named and
// the person grants whichever is missing.
func MissingScope(scope string, err error, also ...string) *Error {
	e := &Error{Class: ClassScope, Scope: scope, err: err}
	needed := append([]string{scope}, also...)
	if len(needed) == 1 {
		e.Message = fmt.Sprintf(
			"Missing required OAuth scope: %s. Run `google-chat-mcp login` again to grant it.", scope)
		return e
	}
	e.Message = fmt.Sprintf(
		"Missing a required OAuth scope. This call needs all of: %s. "+
			"Google does not say which one was declined, so grant whichever is missing and run "+
			"`google-chat-mcp login` again.", strings.Join(needed, ", "))
	return e
}

// needsScope classifies err but names a different scope than the call
// that failed.
//
// The scope on the error is the one the refused call needed, and that
// is what a person should grant — with one exception: a read taken on
// the way to a write. Granting what the read wanted gets them past this
// call and refused by the next one. Its only caller says why it is one.
func needsScope(err error, scope string) error {
	if gchat.IsMissingScope(err) {
		return MissingScope(scope, err)
	}
	return Classify(err)
}

// Classify turns an error from the client into a tool-facing one.
//
// The scope a refusal names comes with the error. It is a property of
// the endpoint, so internal/gchat records it beside the call and carries
// it out on *APIError; every caller here restating it was thirty-three
// chances to name the wrong one.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		return already
	}

	// A refusal to hand out an access token never reached Google, so it
	// carries no API error. It is still the most common failure a person
	// sees, and the answer is always the same: sign in again.
	if errors.Is(err, auth.ErrReauthorize) {
		return &Error{
			Class:   ClassAuth,
			Message: "Not signed in, or the stored credentials no longer work. Run `google-chat-mcp login --client-secret <path>`.",
			err:     err,
		}
	}

	var apiErr *gchat.APIError
	if !errors.As(err, &apiErr) {
		return &Error{Class: ClassUnexpected, Message: err.Error(), err: err}
	}

	switch {
	case gchat.IsMissingScope(err):
		return MissingScope(apiErr.Scope, err, apiErr.AlsoScopes...)
	case gchat.IsUnauthorized(err):
		return &Error{
			Class:   ClassAuth,
			Message: "Google rejected the stored credentials. Run `google-chat-mcp login` to sign in again.",
			err:     err,
		}
	case gchat.IsNotFound(err):
		return &Error{Class: ClassNotFound, Message: apiErr.Message, err: err}
	case gchat.IsQuotaExceeded(err):
		return &Error{
			Class: ClassQuota,
			Message: "A Google quota for this project is spent, not a rate limit. Waiting a few seconds " +
				"will not help; a daily quota resets on Google's own schedule.",
			err: err,
		}
	case gchat.IsRateLimited(err):
		return &Error{
			Class:   ClassRateLimit,
			Message: "Google is rate limiting this account. Wait a moment and try again.",
			err:     err,
		}
	case gchat.IsForbidden(err):
		// Not "this is a permission problem": Google answers a deleted
		// space with this same 403, saying "or the resource doesn't
		// exist" in its own message. Asserting which one it is would be
		// telling a caller something this server does not know.
		return &Error{
			Class: ClassForbidden,
			Message: fmt.Sprintf("Google refused this: %s. The scopes are granted, so it is not a consent "+
				"problem — either the permission is missing or the thing is not there. Retrying unchanged "+
				"will not help.", apiErr.Message),
			err: err,
		}
	case apiErr.StatusCode >= 500:
		return &Error{
			Class: ClassServer,
			Message: fmt.Sprintf("Google failed rather than refused: %d %s. The request was not wrong, "+
				"so trying again shortly is reasonable.", apiErr.StatusCode, apiErr.Status),
			err: err,
		}
	case apiErr.StatusCode == 400:
		return &Error{Class: ClassInvalid, Message: apiErr.Message, err: err}
	default:
		return &Error{
			Class:   ClassUpstream,
			Message: fmt.Sprintf("Google returned %d %s: %s", apiErr.StatusCode, apiErr.Status, apiErr.Message),
			err:     err,
		}
	}
}
