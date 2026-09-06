package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/config"
)

// chatAndPeople splits Chat calls from People calls, which every
// message- or member-shaped tool makes in turn.
func chatAndPeople(chat, people http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/people") {
			people(w, r)
			return
		}
		chat(w, r)
	}
}

func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, s) }
}

const messagePage = `{"messages":[{"name":"spaces/A/messages/1","sender":{"name":"users/1","displayName":"Jane Doe"},
  "createTime":"2026-01-02T03:04:05Z","text":"hello","thread":{"name":"spaces/A/threads/T1"}}]}`

// searchPage is the same message as Google's search returns it, wrapped
// in the result rows that carry the read and mute state.
const searchPage = `{"results":[{"message":{"name":"spaces/A/messages/1",
  "sender":{"name":"users/1","displayName":"Jane Doe"},"createTime":"2026-01-02T03:04:05Z",
  "text":"hello","thread":{"name":"spaces/A/threads/T1"}}}]}`

// peopleBatch answers a batch People lookup with the same person for
// everyone it was asked about, the way Google echoes the resource names
// back. The logging test needs the same shape with its own values, so
// the payload is built here rather than written out per test.
func peopleBatch(email, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := make([]string, 0, 4)
		for _, resource := range r.URL.Query()["resourceNames"] {
			entries = append(entries, fmt.Sprintf(
				`{"requestedResourceName":%q,"person":{"emailAddresses":[{"value":%q}],`+
					`"names":[{"displayName":%q}]}}`, resource, email, name))
		}
		fmt.Fprintf(w, `{"responses":[%s]}`, strings.Join(entries, ","))
	}
}

// peopleSearch answers a directory search with one hit.
func peopleSearch(resource, email, name string) string {
	return fmt.Sprintf(`{"people":[{"resourceName":%q,"emailAddresses":[{"value":%q}],`+
		`"names":[{"displayName":%q}]}]}`, resource, email, name)
}

// personHit is peopleBatch with the fixture values every read test uses.
var personHit = peopleBatch("janedoe@example.com", "Jane Doe")

func TestGetSpaceThroughASession(t *testing.T) {
	cs := session(t, body(`{"name":"spaces/A","spaceType":"SPACE","displayName":"Team","createTime":"2026-01-02T03:04:05Z"}`))
	var out SpaceDetailOutput
	call(t, cs, "get_space", map[string]any{"space_id": "spaces/A"}, &out)
	if out.SpaceID != "spaces/A" || out.Type != "SPACE" || out.DisplayName != "Team" {
		t.Errorf("get_space = %+v", out)
	}
	if out.CreateTime == nil {
		t.Error("create time should reach the model")
	}
	if out.SingleUserBotDM != nil {
		t.Error("a flag Google did not send must arrive as null, not false")
	}
}

func TestGetMessagesThroughASession(t *testing.T) {
	cs := session(t, chatAndPeople(body(messagePage), personHit))
	var out MessageListOutput
	call(t, cs, "get_messages", map[string]any{"space_id": "spaces/A", "limit": 5}, &out)
	if len(out.Result) != 1 {
		t.Fatalf("get_messages = %+v", out)
	}
	row := out.Result[0]
	if row.MessageID != "spaces/A/messages/1" || row.ThreadID != "spaces/A/threads/T1" {
		t.Errorf("row = %+v", row)
	}
	if row.SenderEmail == nil || *row.SenderEmail != "janedoe@example.com" {
		t.Errorf("email = %v", row.SenderEmail)
	}
}

// The degrade rule, seen from the model's side: the row is there and
// the email is explicitly null rather than an empty string it might
// treat as an address.
func TestAPeopleFailureShowsAsANullEmail(t *testing.T) {
	cs := session(t, chatAndPeople(body(messagePage), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
	}))
	res := call(t, cs, "get_messages", map[string]any{"space_id": "spaces/A"}, nil)
	if res.IsError {
		t.Fatalf("a People failure must not fail the tool: %s", errorText(t, res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"sender_email":null`) {
		t.Errorf("result = %s, want an explicit null email", raw)
	}
	if !strings.Contains(string(raw), `"spaces/A/messages/1"`) {
		t.Errorf("result = %s, want the message kept", raw)
	}
}

func TestGetThreadThroughASession(t *testing.T) {
	cs := session(t, chatAndPeople(body(messagePage), personHit))
	var out MessageListOutput
	call(t, cs, "get_thread", map[string]any{
		"space_id": "spaces/A", "thread_name": "spaces/A/threads/T1",
	}, &out)
	if len(out.Result) != 1 {
		t.Errorf("get_thread = %+v", out)
	}
}

func TestGetMessageThroughASession(t *testing.T) {
	const one = `{"name":"spaces/A/messages/1","sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z",
	  "thread":{"name":"spaces/A/threads/T1"},"text":"hello",
	  "emojiReactionSummaries":[{"emoji":{"unicode":"👍"},"reactionCount":3}]}`
	cs := session(t, chatAndPeople(body(one), personHit))
	var out MessageDetailOutput
	call(t, cs, "get_message", map[string]any{"message_name": "spaces/A/messages/1"}, &out)
	if out.SpaceID != "spaces/A" {
		t.Errorf("space = %q", out.SpaceID)
	}
	if len(out.Reactions) != 1 || out.Reactions[0].Count != 3 {
		t.Errorf("reactions = %+v", out.Reactions)
	}
	if out.LastUpdateTime != nil {
		t.Error("a message that was never edited should have a null edit time")
	}
}

func TestListMembersThroughASession(t *testing.T) {
	const page = `{"memberships":[
	  {"name":"spaces/A/members/1","state":"JOINED","role":"ROLE_MEMBER","member":{"name":"users/1","displayName":"Jane Doe"}},
	  {"name":"spaces/A/members/2","state":"JOINED","role":"ROLE_MEMBER","groupMember":{"name":"groups/G1"}}
	]}`
	cs := session(t, chatAndPeople(body(page), personHit))
	var out MemberListOutput
	call(t, cs, "list_members", map[string]any{"space_id": "spaces/A"}, &out)
	if len(out.Result) != 2 {
		t.Fatalf("list_members = %+v", out)
	}
	if out.Result[0].Kind != "HUMAN" || out.Result[1].Kind != "GROUP" {
		t.Errorf("kinds = %q, %q", out.Result[0].Kind, out.Result[1].Kind)
	}
	if out.Result[1].Email != nil {
		t.Error("a group has no email")
	}
}

func TestListReactionsThroughASession(t *testing.T) {
	cs := session(t, body(`{"reactions":[{"name":"spaces/A/messages/1/reactions/R1","emoji":{"unicode":"👍"},"user":{"name":"users/1"}}]}`))
	var out ListReactionsOutput
	call(t, cs, "list_reactions", map[string]any{"message_name": "spaces/A/messages/1"}, &out)
	if len(out.Reactions) != 1 || out.Reactions[0].Emoji != "👍" {
		t.Errorf("list_reactions = %+v", out)
	}
	if out.NextPageToken != nil {
		t.Error("a single page should report no next page")
	}
}

func TestSearchMessagesThroughASession(t *testing.T) {
	cs := session(t, body(searchPage))
	var out SearchMessagesOutput
	call(t, cs, "search_messages", map[string]any{"query": "hello"}, &out)
	if len(out.Matches) != 1 || out.Matches[0].Snippet != "hello" {
		t.Errorf("search_messages = %+v", out)
	}
	if !out.ServerSide {
		t.Error("a query goes to Google, and the result should say so")
	}
}

// The regex mode is the local scan, over one space, and it is the only
// one that reads pages of history.
func TestARegexSearchIsScannedHere(t *testing.T) {
	cs := session(t, body(messagePage))
	var out SearchMessagesOutput
	call(t, cs, "search_messages", map[string]any{"space_id": "spaces/A", "regex": "h.llo"}, &out)
	if len(out.Matches) != 1 {
		t.Errorf("search_messages = %+v", out)
	}
	if out.ServerSide {
		t.Error("a regex cannot be sent to Google, which has no pattern syntax")
	}
	if out.Scanned != 1 {
		t.Errorf("scanned = %d", out.Scanned)
	}
}

// A timestamp a model is likely to send has to work, and one that
// cannot be read has to say so as an argument error.
func TestSearchMessagesTakesADate(t *testing.T) {
	cs := session(t, body(searchPage))
	var out SearchMessagesOutput
	call(t, cs, "search_messages", map[string]any{
		"query": "hello", "created_after": "2026-01-01",
	}, &out)
	if len(out.Matches) != 1 {
		t.Errorf("a bare date should be accepted: %+v", out)
	}
	res := call(t, cs, "search_messages", map[string]any{
		"query": "hello", "created_after": "last tuesday",
	}, nil)
	if text := errorText(t, res); !strings.HasPrefix(text, "[invalid]") {
		t.Errorf("error = %q, want the invalid class", text)
	}
}

func TestSearchPeopleThroughASession(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "searchContacts") {
			fmt.Fprint(w, `{"results":[]}`)
			return
		}
		fmt.Fprint(w, peopleSearch("people/123", "janedoe@example.com", "Jane Doe"))
	})
	var out SearchPeopleOutput
	call(t, cs, "search_people", map[string]any{"query": "jane"}, &out)
	if out.TotalReturned != 1 || len(out.People) != 1 {
		t.Fatalf("search_people = %+v", out)
	}
	if out.People[0].UserID == nil || *out.People[0].UserID != "users/123" {
		t.Errorf("user id = %q", deref(out.People[0].UserID))
	}
	if len(out.SourcesAttempted) != 2 || len(out.SourcesSucceeded) != 2 {
		t.Errorf("sources = %+v / %+v", out.SourcesAttempted, out.SourcesSucceeded)
	}
}

func TestListSectionsThroughASession(t *testing.T) {
	cs := session(t, body(`{"sections":[{"name":"users/me/sections/S1","type":"DEFAULT_SPACES"}]}`))
	var out ListSectionsOutput
	call(t, cs, "list_sections", nil, &out)
	if len(out.Sections) != 1 || out.Sections[0].DisplayName != "(spaces)" {
		t.Errorf("list_sections = %+v", out)
	}
	if out.Unparsed != 0 {
		t.Errorf("unparsed = %d", out.Unparsed)
	}
}

func TestListSectionItemsThroughASession(t *testing.T) {
	cs := session(t, body(`{"sectionItems":[{"name":"users/me/sections/S1/items/spaces/A","space":"spaces/A"}]}`))
	var out ListSectionItemsOutput
	call(t, cs, "list_section_items", map[string]any{"space_id": "spaces/A"}, &out)
	if len(out.Items) != 1 || out.Items[0].SectionName != "users/me/sections/S1" {
		t.Errorf("list_section_items = %+v", out)
	}
	if out.Items[0].SpaceID == nil || *out.Items[0].SpaceID != "spaces/A" {
		t.Errorf("space = %v", out.Items[0].SpaceID)
	}
	// Neither selector is an argument error, caught before Google.
	res := call(t, cs, "list_section_items", map[string]any{}, nil)
	if text := errorText(t, res); !strings.HasPrefix(text, "[invalid]") {
		t.Errorf("error = %q", text)
	}
}

// readTools is the whole read-only surface. It is written out rather
// than derived, because deriving it from the annotations would make the
// test agree with whatever the code says.
var readTools = []string{
	"find_group_chats",
	"get_availability", "get_custom_emoji", "get_member", "get_message", "get_messages", "get_space",
	"get_space_event", "get_space_notification_setting", "get_space_read_state",
	"get_thread", "get_thread_read_state",
	"list_custom_emojis", "list_members", "list_pinned_messages", "list_reactions",
	"list_section_items", "list_sections", "list_space_events", "list_spaces",
	"search_messages", "search_people", "search_spaces", "whoami",
}

// Every read tool has to be registered and marked read-only, and
// nothing else may claim that hint: a client that trusts it runs the
// tool without asking anyone.
func TestEveryReadToolIsRegistered(t *testing.T) {
	cs := session(t, body(`{}`))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	have := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		have[tool.Name] = tool
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %q has no output schema", tool.Name)
		}
	}
	for _, want := range readTools {
		tool, ok := have[want]
		if !ok {
			t.Errorf("tool %q is not registered", want)
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not marked read-only", want)
		}
	}
	for name, tool := range have {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			continue
		}
		if !slices.Contains(readTools, name) {
			t.Errorf("tool %q claims to be read-only but is not one of the read tools", name)
		}
	}
}

// download_attachment reads from Chat and writes a file, so it is
// neither of the two kinds this suite otherwise checks: it must not
// claim to be read-only, and GCM_READ_ONLY must still register it,
// because that flag is about Chat.
func TestAToolThatWritesLocallyIsNeitherReadOnlyNorLeftOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
	}{
		{"ordinarily", config.Config{Toolsets: config.DefaultToolsets()}},
		{"under GCM_READ_ONLY", config.Config{Toolsets: config.DefaultToolsets(), ReadOnly: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := sessionWithConfig(t, body(`{}`), tc.cfg)
			list, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			var found *mcp.Tool
			for _, tool := range list.Tools {
				if tool.Name == "download_attachment" {
					found = tool
				}
			}
			if found == nil {
				t.Fatal("download_attachment is not registered")
			}
			if found.Annotations == nil || found.Annotations.ReadOnlyHint {
				t.Error("download_attachment claims to touch nothing, and it writes a file")
			}
		})
	}
}

// Turning the sections toolset off has to remove exactly its tools.
func TestSectionToolsCanBeLeftOut(t *testing.T) {
	// Everything but sections, so the comparison isolates one toolset
	// rather than every non-core one at once.
	var withoutSections []config.Toolset
	for _, t := range config.AllToolsets {
		if t != config.ToolsetSections {
			withoutSections = append(withoutSections, t)
		}
	}
	cs := sessionWithToolsets(t, body(`{}`), withoutSections...)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range list.Tools {
		if strings.Contains(tool.Name, "section") {
			t.Errorf("tool %q should not be registered without its toolset", tool.Name)
		}
	}
	// Seven tools are the sidebar, whatever the surface grows to
	// elsewhere.
	full := sessionWithToolsets(t, body(`{}`), config.AllToolsets...)
	all, err := full.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if want := len(all.Tools) - 7; len(list.Tools) != want {
		t.Errorf("%d tools without the sections toolset, want %d", len(list.Tools), want)
	}
}

// A tool call that fails must not be a JSON-RPC error: the protocol
// reserves those for the caller getting the request wrong.
func TestAnUpstreamFailureIsAToolError(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"status":"NOT_FOUND","message":"no such space"}}`)
	})
	res := call(t, cs, "get_space", map[string]any{"space_id": "spaces/gone"}, nil)
	if text := errorText(t, res); !strings.HasPrefix(text, "[not_found]") {
		t.Errorf("error = %q", text)
	}
}

// A misspelled argument must fail rather than be dropped. On a write
// tool the same rule is what stops a typo'd dry_run from posting.
func TestUnknownArgumentsAreRejectedEverywhere(t *testing.T) {
	cs := session(t, body(`{}`))
	for _, name := range []string{"list_spaces", "get_space", "get_messages", "list_members", "search_people"} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      name,
			Arguments: map[string]any{"space_id": "spaces/A", "query": "x", "not_a_field": 1},
		})
		if err == nil && !res.IsError {
			t.Errorf("%s accepted an unknown argument", name)
		}
	}
}

// An empty answer from Google is ordinary — a quiet space, a section
// with nothing in it — and the SDK validates every result against the
// output schema. A tool that returns a nil slice where the schema says
// array fails there rather than at the caller.
func TestEveryReadToolSurvivesAnEmptyAnswer(t *testing.T) {
	cs := session(t, body(`{}`))
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_spaces", nil},
		{"get_space", map[string]any{"space_id": "spaces/A"}},
		{"list_members", map[string]any{"space_id": "spaces/A"}},
		{"get_messages", map[string]any{"space_id": "spaces/A"}},
		{"get_thread", map[string]any{"space_id": "spaces/A", "thread_name": "spaces/A/threads/T"}},
		{"list_reactions", map[string]any{"message_name": "spaces/A/messages/1"}},
		{"search_messages", map[string]any{"space_id": "spaces/A", "query": "x"}},
		{"search_people", map[string]any{"query": "x"}},
		{"list_sections", nil},
		{"list_section_items", map[string]any{"space_id": "spaces/A"}},
		{"whoami", nil},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			res := call(t, cs, tc.tool, tc.args, nil)
			if res.IsError {
				t.Errorf("%s on an empty answer: %s", tc.tool, errorText(t, res))
			}
		})
	}
}

// get_message is the one read that cannot answer with nothing. A
// message with no resource name is not a message, and saying so beats
// handing the model an object with an empty id.
func TestGetMessageOnAnEmptyAnswer(t *testing.T) {
	cs := session(t, body(`{}`))
	res := call(t, cs, "get_message", map[string]any{"message_name": "spaces/A/messages/1"}, nil)
	if text := errorText(t, res); !strings.HasPrefix(text, "[upstream]") {
		t.Errorf("error = %q, want it reported as an upstream oddity", text)
	}
}

// The administrator's scope is requested at login only when the admin
// toolset is on, so without it the call would reach Google and be
// refused as a permission problem. The refusal has to name the thing to
// change instead.
func TestAdminSearchIsRefusedWithoutItsToolset(t *testing.T) {
	cs := session(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an admin search must not reach Google without the toolset")
	})
	res := call(t, cs, "search_spaces",
		map[string]any{"display_name": "Engineering", "use_admin_access": true}, nil)
	text := errorText(t, res)
	if !strings.Contains(text, "admin") || !strings.Contains(text, "TOOLSETS") {
		t.Errorf("the refusal %q does not say what to change", text)
	}

	// And with the toolset on it is an ordinary search.
	var reached bool
	on := sessionWithToolsets(t, func(w http.ResponseWriter, r *http.Request) {
		reached = r.URL.Query().Get("useAdminAccess") == "true"
		fmt.Fprint(w, `{"results":[]}`)
	}, config.AllToolsets...)
	if res := call(t, on, "search_spaces",
		map[string]any{"display_name": "Engineering", "use_admin_access": true}, nil); res.IsError {
		t.Fatalf("admin search with the toolset on: %s", errorText(t, res))
	}
	if !reached {
		t.Error("the admin flag did not reach Google")
	}
}
