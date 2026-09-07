package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/internal/service"
)

// fakeChat stands in for the service. The walk is about which calls it
// makes and what it does with a refusal, neither of which needs Google.
type fakeChat struct {
	identity *service.Identity
	spaces   []service.SpaceSummary
	messages []service.MessageRow
	members  []service.Member
	sections []service.Section
	drift    []string

	whoamiErr, spacesErr, messagesErr, membersErr, sectionsErr error

	messageCalls, memberCalls int
}

func (f *fakeChat) Whoami(context.Context) (*service.Identity, error) {
	return f.identity, f.whoamiErr
}

func (f *fakeChat) ListSpaces(context.Context, service.ListSpacesInput) (*service.ListSpacesResult, error) {
	if f.spacesErr != nil {
		return nil, f.spacesErr
	}
	return &service.ListSpacesResult{Spaces: f.spaces}, nil
}

func (f *fakeChat) GetMessages(context.Context, service.GetMessagesInput) (*service.MessagesResult, error) {
	f.messageCalls++
	if f.messagesErr != nil {
		return nil, f.messagesErr
	}
	return &service.MessagesResult{Messages: f.messages}, nil
}

func (f *fakeChat) ListMembers(context.Context, service.ListMembersInput) (*service.MembersResult, error) {
	f.memberCalls++
	if f.membersErr != nil {
		return nil, f.membersErr
	}
	return &service.MembersResult{Members: f.members}, nil
}

func (f *fakeChat) ListSections(context.Context, service.ListSectionsInput) (*service.ListSectionsResult, error) {
	if f.sectionsErr != nil {
		return nil, f.sectionsErr
	}
	return &service.ListSectionsResult{Sections: f.sections}, nil
}

func (f *fakeChat) DriftPaths() []string { return f.drift }

func healthy() *fakeChat {
	return &fakeChat{
		identity: &service.Identity{Email: "janedoe@example.com", UserSub: "12345"},
		spaces:   []service.SpaceSummary{{Name: "spaces/A"}, {Name: "spaces/B"}},
		messages: []service.MessageRow{{Name: "spaces/A/messages/1"}},
		members:  []service.Member{{Name: "users/1"}},
		sections: []service.Section{{Name: "users/me/sections/S1"}},
	}
}

func TestRunSamplesEverySpace(t *testing.T) {
	f := healthy()
	got, err := Run(context.Background(), f, 2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Spaces != 2 || got.Messages != 2 || got.Members != 2 || got.Sections != 1 {
		t.Errorf("report = %+v", got)
	}
	if f.messageCalls != 2 || f.memberCalls != 2 {
		t.Errorf("%d message and %d member calls, want one per space", f.messageCalls, f.memberCalls)
	}
	if len(got.Skipped) != 0 {
		t.Errorf("skipped = %+v", got.Skipped)
	}
}

// Nobody signed in is the one failure worth stopping for: there is
// nothing to check against.
func TestRunStopsWhenNobodyIsSignedIn(t *testing.T) {
	f := healthy()
	f.whoamiErr = service.Failf(service.ClassAuth, "not signed in")
	if _, err := Run(context.Background(), f, 1); err == nil {
		t.Fatal("want an error")
	}
}

// Everything after the identity check is diagnostic. A person running
// doctor because one scope is missing should still learn about the
// others.
func TestARefusedScopeDoesNotStopTheWalk(t *testing.T) {
	f := healthy()
	f.messagesErr = service.Failf(service.ClassScope, "Missing required OAuth scope: chat.messages.readonly")
	got, err := Run(context.Background(), f, 2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Members != 2 {
		t.Errorf("members = %d, want the other checks to have run", got.Members)
	}
	if len(got.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want the same reason recorded once", got.Skipped)
	}
	if !strings.Contains(got.Skipped[0], "scope") {
		t.Errorf("skipped = %q, want the class in it", got.Skipped[0])
	}
}

func TestSpacesFailureEndsTheWalkWithoutAnError(t *testing.T) {
	f := healthy()
	f.spacesErr = service.Failf(service.ClassScope, "no spaces scope")
	got, err := Run(context.Background(), f, 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Spaces != 0 || len(got.Skipped) != 1 {
		t.Errorf("report = %+v", got)
	}
}

func TestRunReportsSectionRefusalSeparately(t *testing.T) {
	f := healthy()
	f.sectionsErr = errors.New("plain failure")
	got, err := Run(context.Background(), f, 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "plain failure") {
		t.Errorf("skipped = %+v", got.Skipped)
	}
}

func TestRunTakesAtLeastOneSpace(t *testing.T) {
	f := healthy()
	if _, err := Run(context.Background(), f, 0); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.messageCalls == 0 {
		t.Error("a sample of zero should be treated as one")
	}
}

// The report is the whole output of the command, so what it prints is
// the contract with the person reading it.
func TestReportNamesEveryDriftedField(t *testing.T) {
	f := healthy()
	f.drift = []string{"Message.newField", "Space.spaceDetails.newThing"}
	got, err := Run(context.Background(), f, 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out strings.Builder
	if _, err := got.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	text := out.String()
	for _, want := range []string{"janedoe@example.com", "Message.newField", "Space.spaceDetails.newThing"} {
		if !strings.Contains(text, want) {
			t.Errorf("report does not mention %q:\n%s", want, text)
		}
	}
}

func TestReportSaysWhenNothingDrifted(t *testing.T) {
	got, err := Run(context.Background(), healthy(), 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out strings.Builder
	if _, err := got.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if !strings.Contains(out.String(), "no schema drift") {
		t.Errorf("report = %s", out.String())
	}
}

func TestReportNamesTheChecksItSkipped(t *testing.T) {
	f := healthy()
	f.membersErr = service.Failf(service.ClassScope, "no memberships scope")
	got, err := Run(context.Background(), f, 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out strings.Builder
	if _, err := got.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if !strings.Contains(out.String(), "list members") {
		t.Errorf("report = %s", out.String())
	}
}

// An account with no email still identifies itself by subject id.
func TestReportHandlesAnAccountWithNoEmail(t *testing.T) {
	f := healthy()
	f.identity = &service.Identity{UserSub: "12345"}
	got, err := Run(context.Background(), f, 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out strings.Builder
	if _, err := got.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if !strings.Contains(out.String(), "12345") {
		t.Errorf("report = %s", out.String())
	}
}

// A sample wider than one page of spaces used to fail the whole
// command, which is the opposite of what a diagnostic should do.
func TestRunClampsAWideSample(t *testing.T) {
	f := healthy()
	got, err := Run(context.Background(), f, 100000)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Spaces != 2 {
		t.Errorf("report = %+v", got)
	}
}
