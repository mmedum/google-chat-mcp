package tools

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// The rendering has to carry every fact the JSON carries, and no gate
// can check that. These are the facts that would be missed silently: a
// dry run that reads like a real write, a repeat delete that reads like
// a first one, and a field Google did not send printed as an empty
// string the model would take for a value.
func TestRenderings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		out   renderer
		want  []string
		avoid []string // must not appear
	}{
		{
			name: "a message carries its text, not just its id",
			out: MessageOutput{
				MessageID: "spaces/AAAAspace1/messages/AAAAmsg1",
				Timestamp: at("2026-01-02T03:04:05Z"), SenderUserID: "users/1",
				SenderDisplayName: ptr("Jane Doe"), SenderEmail: ptr("janedoe@example.com"),
				Text: "the standup moved to 10", ThreadID: "spaces/AAAAspace1/threads/AAAAthread1",
			},
			want: []string{"spaces/AAAAspace1/messages/AAAAmsg1", "2026-01-02T03:04:05Z",
				"Jane Doe <janedoe@example.com>", "the standup moved to 10"},
		},
		{
			name: "a sender Google could not name falls back to the id",
			out: MessageOutput{
				MessageID: "spaces/AAAAspace1/messages/AAAAmsg1",
				Timestamp: at("2026-01-02T03:04:05Z"), SenderUserID: "users/1", Text: "hello",
			},
			want:  []string{"users/1"},
			avoid: []string{"<>", "()", " · ·"},
		},
		{
			name:  "an empty listing says so rather than trailing a colon",
			out:   ListSpacesOutput{},
			want:  []string{"0 spaces."},
			avoid: []string{":"},
		},
		{
			name: "a listing counts what it holds",
			out: ListSpacesOutput{Result: []SpaceSummaryOutput{
				{SpaceID: "spaces/AAAAspace1", Type: "SPACE", DisplayName: "Team standup"},
			}},
			want: []string{"1 space:", "spaces/AAAAspace1 · SPACE · Team standup"},
		},
		{
			name:  "a flag Google did not send is left out, not printed as false",
			out:   SpaceDetailOutput{SpaceID: "spaces/AAAAspace1", Type: "SPACE", DisplayName: "Team standup"},
			want:  []string{"spaces/AAAAspace1 · SPACE · Team standup"},
			avoid: []string{"external users allowed", "created"},
		},
		{
			name: "a flag Google did send is reported either way",
			out: SpaceDetailOutput{
				SpaceID: "spaces/AAAAspace1", Type: "SPACE", DisplayName: "Team standup",
				ExternalUserAllowed: ptr(false), CreateTime: ptr(at("2026-01-02T03:04:05Z")),
			},
			want: []string{"external users allowed: no", "created 2026-01-02T03:04:05Z"},
		},
		{
			name:  "a dry run never reads like a write that happened",
			out:   SendMessageOutput{SpaceID: "spaces/AAAAspace1", DryRun: true},
			want:  []string{"Dry run", "nothing was sent", "spaces/AAAAspace1"},
			avoid: []string{"Posted"},
		},
		{
			name: "a post says where it landed",
			out: SendMessageOutput{
				MessageID: ptr("spaces/AAAAspace1/messages/AAAAmsg1"), SpaceID: "spaces/AAAAspace1",
				ThreadID: ptr("spaces/AAAAspace1/threads/AAAAthread1"),
			},
			want: []string{"Posted spaces/AAAAspace1/messages/AAAAmsg1", "spaces/AAAAspace1/threads/AAAAthread1"},
		},
		{
			name:  "a repeat delete does not claim to have deleted anything",
			out:   DeleteMessageOutput{MessageName: "spaces/AAAAspace1/messages/AAAAmsg1"},
			want:  []string{"was already gone", "nothing was deleted"},
			avoid: []string{"Deleted spaces"},
		},
		{
			name:  "a delete that landed says so",
			out:   DeleteMessageOutput{MessageName: "spaces/AAAAspace1/messages/AAAAmsg1", Deleted: true},
			want:  []string{"Deleted spaces/AAAAspace1/messages/AAAAmsg1."},
			avoid: []string{"already gone"},
		},
		{
			name: "a dry run that would change nothing does not promise a request body",
			out: MoveSpaceToSectionOutput{
				SpaceID: "spaces/AAAAspace1", SectionName: "users/me/sections/AAAAsection1",
				FromSection: "users/me/sections/AAAAsection1", DryRun: true,
			},
			want:  []string{"is already in", "nothing was moved"},
			avoid: []string{"Dry run", "request body"},
		},
		{
			name: "a dry run that would move says so",
			out: MoveSpaceToSectionOutput{
				SpaceID: "spaces/AAAAspace1", SectionName: "users/me/sections/AAAAsection2",
				FromSection: "users/me/sections/AAAAsection1", DryRun: true,
				RenderedPayload: rendered(map[string]any{"section": "users/me/sections/AAAAsection2"}),
			},
			want: []string{"Dry run", "request body"},
		},
		{
			name: "a move that was a no-op says the space was already there",
			out: MoveSpaceToSectionOutput{
				SpaceID: "spaces/AAAAspace1", SectionName: "users/me/sections/AAAAsection1",
				ItemName: "items/AAAAitem1", FromSection: "users/me/sections/AAAAsection1",
			},
			want:  []string{"is already in", "nothing was moved"},
			avoid: []string{"Moved spaces"},
		},
		{
			name: "a local scan reports how much it read, so an empty answer means something",
			out:  SearchMessagesOutput{Scanned: 240, CapReached: true},
			want: []string{"0 matches in 240 messages scanned here.", "There is more than this"},
		},
		{
			name: "a partial listing admits it",
			out:  ListSectionsOutput{Unparsed: 2, NextPageToken: ptr("tok")},
			want: []string{"0 sections.", "2 rows were not understood", "incomplete", "More to come"},
		},
		{
			name: "a people search names the source that did not answer",
			out: SearchPeopleOutput{
				TotalReturned:    1,
				People:           []PersonHitOutput{{UserID: ptr("users/1"), Email: ptr("janedoe@example.com"), Source: "DIRECTORY"}},
				SourcesAttempted: []string{"DIRECTORY", "CONTACTS"}, SourcesSucceeded: []string{"DIRECTORY"},
			},
			want: []string{"1 person:", "janedoe@example.com", "may be short"},
		},
		{
			name:  "a reaction that was not there is not reported as removed",
			out:   RemoveReactionOutput{},
			want:  []string{"No matching reaction"},
			avoid: []string{"Removed"},
		},
		{
			name: "adding a reaction names the emoji and the resource",
			out:  AddReactionOutput{ReactionName: "spaces/AAAAspace1/messages/AAAAmsg1/reactions/AAAAreact1", Emoji: "👍", UserID: "users/1"},
			want: []string{"Reacted 👍", "reactions/AAAAreact1"},
		},
		{
			name: "a direct message reports the space to write to",
			out:  FindDirectMessageOutput{SpaceID: "spaces/AAAAdm1"},
			want: []string{"spaces/AAAAdm1"},
		},
		{
			name: "a section reports where it sits",
			out:  SectionOutput{SectionName: "users/me/sections/AAAAsection1", DisplayName: "Clients", Type: "CUSTOM", SortOrder: ptr(3)},
			want: []string{"users/me/sections/AAAAsection1 · Clients · CUSTOM · sort 3"},
		},
		{
			name: "a member is named by whoever Google could identify",
			out: MemberOutput{
				Kind: "HUMAN", MemberID: "spaces/AAAAspace1/members/AAAAmember1",
				DisplayName: ptr("Jane Doe"), Email: ptr("janedoe@example.com"),
				Role: "ROLE_MEMBER", State: "JOINED",
			},
			want: []string{"Jane Doe <janedoe@example.com>", "ROLE_MEMBER", "JOINED"},
		},
		{
			name: "whoami says which account is signed in",
			out:  WhoamiOutput{UserSub: "111111111111111111111", Email: "janedoe@example.com", DisplayName: "Jane Doe"},
			want: []string{"Signed in as", "Jane Doe <janedoe@example.com>"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.out.Render()
			if strings.TrimSpace(got) == "" {
				t.Fatal("rendered nothing")
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.avoid {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

// Every registered tool, driven end to end, so the rule that a reply
// carries both halves is proved for the whole surface rather than for
// whichever tools other tests happen to call. The table is checked
// against the registered tools below, so a tool added without an entry
// fails here rather than shipping unrendered.
func TestEveryToolReturnsBothHalves(t *testing.T) {
	cs := session(t, everyEndpoint)
	for name, in := range everyRegisteredTool(t, cs) {
		t.Run(name, func(t *testing.T) {
			// call asserts both halves for any result that is not an
			// error, which is the whole point of routing through it.
			if res := call(t, cs, name, in, nil); res.IsError {
				t.Fatalf("%s: %s", name, errorText(t, res))
			}
		})
	}
}

// everyEndpoint answers whatever a tool asks for with a payload of the
// right shape. It is deliberately permissive: this test is about the
// reply envelope, and the payload rules are tested per tool elsewhere.
func everyEndpoint(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.Contains(path, "people:searchDirectoryPeople"), strings.Contains(path, "people:searchContacts"):
		fmt.Fprint(w, peopleSearch("people/1", "janedoe@example.com", "Jane Doe"))
	case strings.HasPrefix(path, "/people"):
		personHit(w, r)
	case strings.HasPrefix(path, "/oidc"):
		fmt.Fprint(w, `{"sub":"111111111111111111111","email":"janedoe@example.com","name":"Jane Doe"}`)
	case strings.Contains(path, "/reactions"):
		fmt.Fprint(w, `{"reactions":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1/reactions/AAAAreact1",
		  "emoji":{"unicode":"👍"},"user":{"name":"users/1"}}],
		  "name":"spaces/AAAAspace1/messages/AAAAmsg1/reactions/AAAAreact1",
		  "emoji":{"unicode":"👍"},"user":{"name":"users/1"}}`)
	case strings.Contains(path, "/sectionItems"), strings.Contains(path, "/items"):
		fmt.Fprint(w, `{"items":[{"name":"users/me/sections/AAAAsection1/items/AAAAitem1",
		  "space":{"name":"spaces/AAAAspace1"}}],"name":"users/me/sections/AAAAsection1/items/AAAAitem1"}`)
	case strings.Contains(path, "/sections"):
		fmt.Fprint(w, `{"sections":[{"name":"users/me/sections/AAAAsection1","displayName":"Clients",
		  "sectionType":"CUSTOM","sortOrder":1}],"name":"users/me/sections/AAAAsection1",
		  "displayName":"Clients","sectionType":"CUSTOM","sortOrder":1}`)
	case strings.Contains(path, "/spaceEvents"):
		fmt.Fprint(w, `{"spaceEvents":[{"name":"spaces/AAAAspace1/spaceEvents/AAAAevent1",
		  "eventTime":"2026-01-02T03:04:05Z","eventType":"google.workspace.chat.message.v1.created",
		  "messageCreatedEventData":{"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg1"}}}],
		  "name":"spaces/AAAAspace1/spaceEvents/AAAAevent1","eventTime":"2026-01-02T03:04:05Z",
		  "eventType":"google.workspace.chat.message.v1.created",
		  "messageCreatedEventData":{"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg1"}}}`)
	case strings.Contains(path, "attachments:upload"):
		fmt.Fprint(w, `{"attachmentDataRef":{"attachmentUploadToken":"AAAAuploadtoken1"}}`)
	case strings.HasPrefix(path, "/v1/media/"):
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "the notes")
	case strings.Contains(path, "/messages"):
		fmt.Fprint(w, `{"messages":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1",
		  "sender":{"name":"users/1","displayName":"Jane Doe"},"createTime":"2026-01-02T03:04:05Z",
		  "text":"the standup moved to 10","thread":{"name":"spaces/AAAAspace1/threads/AAAAthread1"}}],
		  "name":"spaces/AAAAspace1/messages/AAAAmsg1","sender":{"name":"users/1","displayName":"Jane Doe"},
		  "createTime":"2026-01-02T03:04:05Z","text":"the standup moved to 10",
		  "thread":{"name":"spaces/AAAAspace1/threads/AAAAthread1"},
		  "attachment":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach1",
		    "contentName":"notes.txt","contentType":"text/plain","source":"UPLOADED_CONTENT",
		    "attachmentDataRef":{"resourceName":"spaces/AAAAspace1/attachments/AAAAmedia1"}}]}`)
	case strings.Contains(path, "/members"):
		fmt.Fprint(w, `{"memberships":[{"name":"spaces/AAAAspace1/members/AAAAmember1","state":"JOINED",
		  "role":"ROLE_MEMBER","member":{"name":"users/1","type":"HUMAN","displayName":"Jane Doe"}}],
		  "name":"spaces/AAAAspace1/members/AAAAmember1","state":"JOINED","role":"ROLE_MEMBER",
		  "member":{"name":"users/1","type":"HUMAN","displayName":"Jane Doe"}}`)
	default:
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/AAAAspace1","spaceType":"SPACE","displayName":"Team standup"}],
		  "name":"spaces/AAAAspace1","spaceType":"SPACE","displayName":"Team standup"}`)
	}
}
