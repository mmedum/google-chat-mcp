package tools

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

// The model reads the leading class, so it has to survive the trip from
// the service through the handler.
func TestFailKeepsTheServiceClass(t *testing.T) {
	got := fail(service.Failf(service.ClassNotFound, "no such space"))
	if got.Error() != "[not_found] no such space" {
		t.Errorf("fail = %q", got.Error())
	}
}

func TestFailUnwrapsAWrappedServiceError(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), service.Failf(service.ClassScope, "need a scope"))
	if got := fail(wrapped).Error(); !strings.HasPrefix(got, "[scope]") {
		t.Errorf("fail = %q, want the scope class to survive wrapping", got)
	}
}

// An error from somewhere this server did not anticipate still has to
// arrive in the same shape, or the model cannot tell them apart.
func TestFailTagsAnUnclassifiedError(t *testing.T) {
	got := fail(errors.New("something odd")).Error()
	if !strings.HasPrefix(got, "[unexpected] ") {
		t.Errorf("fail = %q", got)
	}
	if !strings.Contains(got, "something odd") {
		t.Errorf("fail = %q, want it to keep the original text", got)
	}
}

// Annotations are advice a client shows a person. They have to be
// internally consistent: nothing read-only destroys anything.
func TestAnnotationsAreConsistent(t *testing.T) {
	for _, tc := range []struct {
		kind        Kind
		name        string
		readOnly    bool
		destructive bool
		idempotent  bool
	}{
		{Read, "Read", true, false, false},
		{Write, "Write", false, false, false},
		{WriteIdempotent, "WriteIdempotent", false, false, true},
		{Destructive, "Destructive", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.kind.annotations()
			if a.ReadOnlyHint != tc.readOnly {
				t.Errorf("ReadOnlyHint = %v, want %v", a.ReadOnlyHint, tc.readOnly)
			}
			if tc.readOnly && a.DestructiveHint != nil && *a.DestructiveHint {
				t.Error("a read-only tool cannot be destructive")
			}
			if !tc.readOnly && (a.DestructiveHint == nil || *a.DestructiveHint != tc.destructive) {
				t.Errorf("DestructiveHint = %v, want %v", a.DestructiveHint, tc.destructive)
			}
			if a.IdempotentHint != tc.idempotent {
				t.Errorf("IdempotentHint = %v, want %v", a.IdempotentHint, tc.idempotent)
			}
			// Every tool here reaches Google.
			if a.OpenWorldHint == nil || !*a.OpenWorldHint {
				t.Error("must be open-world")
			}
		})
	}
}

// A nullable output field says "Google said nothing" with null. An
// empty string would read as a value the caller could use — an address
// to write to, a name to show.
func TestNullableDistinguishesAbsentFromEmpty(t *testing.T) {
	if nullable("") != nil {
		t.Error("an empty string must become null")
	}
	if got := nullable("janedoe@example.com"); got == nil || *got != "janedoe@example.com" {
		t.Errorf("nullable = %v", got)
	}
	if nullableTime(time.Time{}) != nil {
		t.Error("the zero time must become null")
	}
	now := time.Now()
	if got := nullableTime(now); got == nil || !got.Equal(now) {
		t.Errorf("nullableTime = %v", got)
	}
}

// The released surface annotates its timestamps, and jsonschema-go does
// not do it on its own.
func TestOutputSchemaAnnotatesTimestamps(t *testing.T) {
	schema := outputSchema[MessageDetailOutput]()
	ts, ok := schema.Properties["timestamp"]
	if !ok {
		t.Fatal("no timestamp property")
	}
	if ts.Format != "date-time" {
		t.Errorf("format = %q, want date-time", ts.Format)
	}
}
