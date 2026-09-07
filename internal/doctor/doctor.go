// Package doctor checks live Chat responses against the models this
// server holds.
//
// Google adds response fields without notice, and a server that
// silently ignores them drifts away from the API until something it
// does read changes shape. The drift decoder makes that visible in the
// logs; this makes it visible on demand, against the caller's own
// spaces, before it matters.
package doctor

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/mmedum/google-chat-mcp/internal/service"
)

// Chat is the part of the service a check needs. It is an interface so
// the walk can be tested without a Chat API at all.
type Chat interface {
	Whoami(ctx context.Context) (*service.Identity, error)
	ListSpaces(ctx context.Context, in service.ListSpacesInput) (*service.ListSpacesResult, error)
	GetMessages(ctx context.Context, in service.GetMessagesInput) (*service.MessagesResult, error)
	ListMembers(ctx context.Context, in service.ListMembersInput) (*service.MembersResult, error)
	ListSections(ctx context.Context, in service.ListSectionsInput) (*service.ListSectionsResult, error)
	DriftPaths() []string
}

// messagesPerSpace and membersPerSpace are how deep each space is
// sampled. Drift lands on every row of a resource at once, so a handful
// of rows finds it and a full read only spends quota.
const (
	messagesPerSpace = 10
	membersPerSpace  = 10
	// maxSampleSpaces is the space listing's own ceiling. A diagnostic
	// command that exists to survive a partial failure must not refuse
	// to run because someone asked for a wider sample than one page.
	maxSampleSpaces = 200
)

// Report is what a run found.
type Report struct {
	// Email and UserSub identify the account that was checked.
	Email   string
	UserSub string
	// Spaces, Messages, Members and Sections are how much was read.
	Spaces   int
	Messages int
	Members  int
	Sections int
	// Dropped counts rows a listing could not use. This is the number
	// the command exists to surface, so throwing it away would defeat
	// the point.
	Dropped int
	// Drift is every unknown response field, by path.
	Drift []string
	// Skipped names the checks that could not run, with the reason.
	// A refused scope is the common one, and it must not stop the rest.
	Skipped []string
}

// Run walks the caller's own spaces and reports what it saw.
//
// Only the identity check is fatal. Everything after it is diagnostic:
// a person running doctor because one scope is missing should still
// learn about the others.
func Run(ctx context.Context, chat Chat, sampleSpaces int) (*Report, error) {
	sampleSpaces = min(max(sampleSpaces, 1), maxSampleSpaces)
	id, err := chat.Whoami(ctx)
	if err != nil {
		return nil, err
	}
	r := &Report{Email: id.Email, UserSub: id.UserSub}

	spaces, err := chat.ListSpaces(ctx, service.ListSpacesInput{Limit: sampleSpaces})
	if err != nil {
		r.skip("list spaces", err)
		r.Drift = chat.DriftPaths()
		return r, nil
	}
	r.Spaces = len(spaces.Spaces)

	for _, sp := range spaces.Spaces {
		msgs, err := chat.GetMessages(ctx, service.GetMessagesInput{Space: sp.Name, Limit: messagesPerSpace})
		if err != nil {
			r.skip("read messages", err)
		} else {
			r.Messages += len(msgs.Messages)
		}

		members, err := chat.ListMembers(ctx, service.ListMembersInput{Space: sp.Name, Limit: membersPerSpace})
		if err != nil {
			r.skip("list members", err)
		} else {
			r.Members += len(members.Members)
		}
	}

	sections, err := chat.ListSections(ctx, service.ListSectionsInput{})
	if err != nil {
		r.skip("list sections", err)
	} else {
		r.Sections = len(sections.Sections)
		r.Dropped += sections.Unparsed
	}

	r.Drift = chat.DriftPaths()
	return r, nil
}

// skip records a check that could not run, once per reason.
func (r *Report) skip(what string, err error) {
	var se *service.Error
	reason := err.Error()
	if errors.As(err, &se) {
		reason = string(se.Class) + ": " + se.Message
	}
	line := what + " — " + reason
	if !slices.Contains(r.Skipped, line) {
		r.Skipped = append(r.Skipped, line)
	}
}

// WriteTo prints the report for a person.
func (r *Report) WriteTo(w io.Writer) (int64, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "signed in as %s (%s)\n", cmp.Or(r.Email, "(none)"), r.UserSub)
	fmt.Fprintf(&b, "read %d space(s), %d message(s), %d member(s), %d section(s)\n",
		r.Spaces, r.Messages, r.Members, r.Sections)
	if r.Dropped > 0 {
		fmt.Fprintf(&b, "%d row(s) could not be read and were skipped\n", r.Dropped)
	}

	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, "\n%d check(s) could not run:\n", len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}

	if len(r.Drift) == 0 {
		fmt.Fprint(&b, "\nno schema drift: every field Google returned is modelled.\n")
		return b.WriteTo(w)
	}
	fmt.Fprintf(&b, "\n%d unknown response field(s). Google has added them and this server "+
		"ignores them; nothing is broken, but a field worth reading needs a wire type:\n", len(r.Drift))
	for _, path := range r.Drift {
		fmt.Fprintf(&b, "  %s\n", path)
	}
	return b.WriteTo(w)
}
