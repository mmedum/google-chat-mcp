package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

func TestListSpaceEventsSendsGooglesFilter(t *testing.T) {
	var gotPath, gotFilter, gotPageSize string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFilter = r.URL.Query().Get("filter")
		gotPageSize = r.URL.Query().Get("pageSize")
		fmt.Fprint(w, `{"spaceEvents":[
		  {"name":"spaces/AAAAspace1/spaceEvents/AAAAevent1","eventTime":"2026-01-02T03:04:05Z",
		   "eventType":"google.workspace.chat.message.v1.created",
		   "messageCreatedEventData":{"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg1"}}},
		  {"name":"spaces/AAAAspace1/spaceEvents/AAAAevent2","eventTime":"2026-01-02T03:05:05Z",
		   "eventType":"google.workspace.chat.message.v1.batchDeleted",
		   "messageBatchDeletedEventData":{"messages":[
		     {"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg2"}},
		     {"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg3"}}]}}],
		  "nextPageToken":"tok"}`)
	})

	got, err := s.ListSpaceEvents(context.Background(), ListSpaceEventsInput{
		Space: "spaces/AAAAspace1",
		Types: []string{"message_created", "message_deleted"},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSpaceEvents: %v", err)
	}
	if want := "/v1/spaces/AAAAspace1/spaceEvents"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	// Plural, parenthesised, joined by OR. Only OR joins event types.
	want := `(event_types:"google.workspace.chat.message.v1.created" OR ` +
		`event_types:"google.workspace.chat.message.v1.deleted")`
	if gotFilter != want {
		t.Errorf("filter = %q, want %q", gotFilter, want)
	}
	if gotPageSize != "10" {
		t.Errorf("page size = %q", gotPageSize)
	}
	if got.NextPageToken != "tok" || len(got.Events) != 2 {
		t.Fatalf("result = %+v", got)
	}

	first := got.Events[0]
	if first.Kind != "message" || first.Batch || len(first.Resources) != 1 {
		t.Errorf("first = %+v", first)
	}
	// A batch event arrives without being asked for, and it covers
	// several resources.
	second := got.Events[1]
	if second.Kind != "message" || !second.Batch || len(second.Resources) != 2 {
		t.Errorf("second = %+v", second)
	}
}

func TestListSpaceEventsTimeBounds(t *testing.T) {
	var gotFilter string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"spaceEvents":[]}`)
	})
	since := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	until := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.ListSpaceEvents(context.Background(), ListSpaceEventsInput{
		Space: "spaces/AAAAspace1", Types: []string{"space_updated"}, Since: since, Until: until,
	}); err != nil {
		t.Fatalf("ListSpaceEvents: %v", err)
	}
	// A single type is not parenthesised, and the times are joined with
	// AND, which is the only join Google accepts between them.
	want := `start_time="` + since + `" AND event_types:"google.workspace.chat.space.v1.updated"` +
		` AND end_time="` + until + `"`
	if gotFilter != want {
		t.Errorf("filter = %q, want %q", gotFilter, want)
	}
}

func TestListSpaceEventsRefusals(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	for _, tc := range []struct {
		name string
		in   ListSpaceEventsInput
		says string
	}{
		{"no space", ListSpaceEventsInput{Types: []string{"message_created"}}, "space_id"},
		{"no event type", ListSpaceEventsInput{Space: "spaces/AAAAspace1"}, "event_types is required"},
		{"an event type that is not one", ListSpaceEventsInput{
			Space: "spaces/AAAAspace1", Types: []string{"message_burned"}}, "not an event type"},
		{
			// Google returns these without being asked and refuses them
			// in a filter, so they are turned away here where the reason
			// can be given.
			name: "a batch type asked for directly",
			in: ListSpaceEventsInput{Space: "spaces/AAAAspace1",
				Types: []string{"google.workspace.chat.message.v1.batchCreated"}},
			says: "batch types",
		},
		{"a time that is not a time", ListSpaceEventsInput{
			Space: "spaces/AAAAspace1", Types: []string{"message_created"}, Since: "yesterday"}, "not a timestamp"},
		{
			// Past the window Google keeps, which would come back empty
			// and read as "nothing happened".
			name: "a start before the 28-day window",
			in: ListSpaceEventsInput{Space: "spaces/AAAAspace1", Types: []string{"message_created"},
				Since: time.Now().AddDate(0, 0, -40).UTC().Format(time.RFC3339)},
			says: "28 days",
		},
		{"a limit above the maximum", ListSpaceEventsInput{
			Space: "spaces/AAAAspace1", Types: []string{"message_created"}, Limit: 500}, "maximum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ListSpaceEvents(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error = %q, want it to mention %q", err, tc.says)
			}
		})
	}
}

// A bare date works here exactly as it does on search_messages: one
// surface, one rule for what a timestamp argument accepts.
func TestListSpaceEventsTakesABareDate(t *testing.T) {
	var gotFilter string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"spaceEvents":[]}`)
	})
	day := time.Now().AddDate(0, 0, -3).UTC().Format("2006-01-02")
	if _, err := s.ListSpaceEvents(context.Background(), ListSpaceEventsInput{
		Space: "spaces/AAAAspace1", Types: []string{"message_created"}, Since: day,
	}); err != nil {
		t.Fatalf("ListSpaceEvents: %v", err)
	}
	if !strings.Contains(gotFilter, `start_time="`+day+`T00:00:00Z"`) {
		t.Errorf("filter = %q, want the bare date expanded", gotFilter)
	}
}

// Google's own strings work as well as the short names, because they
// are what every result reports back.
func TestListSpaceEventsAcceptsGooglesOwnNames(t *testing.T) {
	var gotFilter string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"spaceEvents":[]}`)
	})
	if _, err := s.ListSpaceEvents(context.Background(), ListSpaceEventsInput{
		Space: "spaces/AAAAspace1",
		// The same type twice, spelled both ways: Google refuses a
		// filter naming one field twice, so the duplicate is dropped.
		Types: []string{"google.workspace.chat.message.v1.created", "message_created"},
	}); err != nil {
		t.Fatalf("ListSpaceEvents: %v", err)
	}
	if want := `event_types:"google.workspace.chat.message.v1.created"`; gotFilter != want {
		t.Errorf("filter = %q, want %q", gotFilter, want)
	}
}

// An event with no payload this server models names nothing, and a row
// that names nothing is dropped and counted rather than reported.
func TestAnEventThatNamesNothingIsCounted(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaceEvents":[
		  {"name":"spaces/AAAAspace1/spaceEvents/AAAAevent1","eventTime":"2026-01-02T03:04:05Z",
		   "eventType":"google.workspace.chat.something.v1.happened"},
		  {"name":"spaces/AAAAspace1/spaceEvents/AAAAevent2","eventTime":"2026-01-02T03:05:05Z",
		   "eventType":"google.workspace.chat.space.v1.updated",
		   "spaceUpdatedEventData":{"space":{"name":"spaces/AAAAspace1"}}}]}`)
	})
	got, err := s.ListSpaceEvents(context.Background(), ListSpaceEventsInput{
		Space: "spaces/AAAAspace1", Types: []string{"space_updated"},
	})
	if err != nil {
		t.Fatalf("ListSpaceEvents: %v", err)
	}
	if len(got.Events) != 1 || got.Unparsed != 1 {
		t.Errorf("events = %d, unparsed = %d", len(got.Events), got.Unparsed)
	}
}

// A refusal names every scope the request needed, because Google's
// answer does not say which of them was declined.
func TestASpaceEventRefusalNamesEveryScopeItNeeded(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED",
		  "message":"Request had insufficient authentication scopes."}}`)
	})
	_, err := s.ListSpaceEvents(context.Background(), ListSpaceEventsInput{
		Space: "spaces/AAAAspace1",
		Types: []string{"message_created", "membership_created", "space_updated"},
	})
	assertClass(t, err, ClassScope)
	for _, want := range []string{scopes.MessagesReadonly, scopes.MembershipsReadonly, scopes.SpacesReadonly} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %s", err, want)
		}
	}
}

func TestGetSpaceEvent(t *testing.T) {
	var gotPath string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/spaceEvents/AAAAevent1",
		  "eventTime":"2026-01-02T03:04:05Z","eventType":"google.workspace.chat.membership.v1.deleted",
		  "membershipDeletedEventData":{"membership":{"name":"spaces/AAAAspace1/members/AAAAmember1"}}}`)
	})
	got, err := s.GetSpaceEvent(context.Background(), "spaces/AAAAspace1/spaceEvents/AAAAevent1")
	if err != nil {
		t.Fatalf("GetSpaceEvent: %v", err)
	}
	if want := "/v1/spaces/AAAAspace1/spaceEvents/AAAAevent1"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if got.Kind != "membership" || len(got.Resources) != 1 ||
		got.Resources[0] != "spaces/AAAAspace1/members/AAAAmember1" {
		t.Errorf("event = %+v", got)
	}

	if _, err := s.GetSpaceEvent(context.Background(), "spaces/AAAAspace1"); err == nil {
		t.Error("a name that is not an event was accepted")
	}
}

// Whether an event covers several changes comes from which payload
// Google set, not from ".batch" in the type string. Google's spelling
// has moved before on this API, and a row that reported one change
// while carrying five would read wrong with nothing failing.
func TestBatchIsReadFromThePayloadNotTheTypeString(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaceEvents":[
		  {"name":"spaces/AAAAspace1/spaceEvents/AAAAevent1","eventTime":"2026-01-02T03:04:05Z",
		   "eventType":"google.workspace.chat.message.v1.somethingElseEntirely",
		   "messageBatchCreatedEventData":{"messages":[
		     {"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg1"}},
		     {"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg2"}}]}}]}`)
	})
	got, err := s.ListSpaceEvents(context.Background(), ListSpaceEventsInput{
		Space: "spaces/AAAAspace1", Types: []string{"message_created"},
	})
	if err != nil {
		t.Fatalf("ListSpaceEvents: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("events = %d", len(got.Events))
	}
	if !got.Events[0].Batch || len(got.Events[0].Resources) != 2 {
		t.Errorf("event = %+v, want a batch of two", got.Events[0])
	}
}

// Every event type this server offers names the scope Google wants for
// it, so a type cannot be added without one.
func TestEveryEventTypeNamesAScopeLoginAsksFor(t *testing.T) {
	asked := map[string]bool{}
	for _, s := range scopes.All {
		asked[s] = true
	}
	for _, known := range gchat.EventTypes {
		if known.Short == "" || known.Full == "" {
			t.Errorf("%+v is missing a name", known)
		}
		if !asked[known.Scope] {
			t.Errorf("%s names scope %q, which login does not ask for", known.Short, known.Scope)
		}
	}
}
