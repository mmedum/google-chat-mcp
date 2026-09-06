package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The tool schemas enforce these shapes with a regular
// expression. Losing that check would let a quote reach a Chat filter
// expression, and would trade a clear argument error for a 400 whose
// message says nothing.
func TestResourceNamesAreChecked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		want  string
		valid bool
	}{
		{"a resource name", "spaces/test_1-a.b", "spaces/test_1-a.b", true},
		{"a bare id", "AAAQ", "spaces/AAAQ", true},
		{"surrounding space", "  spaces/AAAQ  ", "spaces/AAAQ", true},
		{"empty", "", "", false},
		{"a quote, which would end a filter clause early", `spaces/A" OR x="`, "", false},
		{"a space character", "spaces/A B", "", false},
		{"a path segment of its own", "spaces/A/../B", "", false},
		{"another kind of resource", "users/me", "", false},
		{"punctuation only", "spaces/...", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requireSpace(tc.in)
			if tc.valid {
				if err != nil {
					t.Fatalf("requireSpace(%q) = %v", tc.in, err)
				}
				if got != tc.want {
					t.Errorf("requireSpace(%q) = %q, want %q", tc.in, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("requireSpace(%q) = %q, want an error", tc.in, got)
			}
			assertClass(t, err, ClassInvalid)
		})
	}
}

func TestMessageAndThreadAndSectionNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func() (string, error)
		valid bool
	}{
		{"a message", func() (string, error) { return requireMessage("spaces/A/messages/M") }, true},
		{"a message with no space", func() (string, error) { return requireMessage("M") }, false},
		{"a message that is a thread", func() (string, error) { return requireMessage("spaces/A/threads/T") }, false},
		{"a quote in a message id", func() (string, error) { return requireMessage(`spaces/A/messages/M"`) }, false},
		{"a thread in its space", func() (string, error) { return requireThread("spaces/A", "spaces/A/threads/T") }, true},
		{"a thread in another space", func() (string, error) { return requireThread("spaces/A", "spaces/B/threads/T") }, false},
		{"a quote in a thread id", func() (string, error) { return requireThread("spaces/A", `spaces/A/threads/T" AND x="`) }, false},
		{"a section", func() (string, error) { return requireSection("users/me/sections/S") }, true},
		{"a section with no user", func() (string, error) { return requireSection("sections/S") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.check()
			if tc.valid && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.valid {
				if err == nil {
					t.Fatal("want an error")
				}
				assertClass(t, err, ClassInvalid)
			}
		})
	}
}

// The check has to happen before the request, or the filter has already
// been sent.
func TestACraftedNameNeverReachesGoogle(t *testing.T) {
	var reached bool
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		fmt.Fprint(w, `{"messages":[]}`)
	})
	_, err := s.GetThread(context.Background(), GetThreadInput{
		Space:  "spaces/A",
		Thread: `spaces/A/threads/T" AND thread.name = "spaces/A/threads/OTHER`,
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "thread_name") {
		t.Errorf("error = %v, want it to name the argument", err)
	}
	if reached {
		t.Error("a crafted filter went out to Google")
	}
}

// Chat's wildcards are a lone hyphen, and they widen a call from one
// resource to every one of them. A caller may not spell one.
func TestAWildcardIsNotAnID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func() error
	}{
		{"every space", func() error { _, err := requireSpace("spaces/-"); return err }},
		{"a bare wildcard read as a space", func() error { _, err := requireSpace("-"); return err }},
		{"every section", func() error { _, err := requireSection("users/me/sections/-"); return err }},
		{"every message in a space", func() error {
			_, err := requireMessage("spaces/AAAAspace1/messages/-")
			return err
		}},
		{"every space's messages", func() error { _, err := requireMessage("spaces/-/messages/AAAAmsg1"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.check(); err == nil {
				t.Error("a wildcard was accepted as a resource id")
			}
		})
	}
}
