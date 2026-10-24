package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mmedum/google-chat-mcp/v7/internal/config"
)

// counter records the method of every request a tool call made. A dry
// run's whole promise is that none of them writes, and most make none
// at all.
type counter struct {
	mu      sync.Mutex
	methods []string
}

func (c *counter) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.methods = append(c.methods, r.Method)
		c.mu.Unlock()
		h(w, r)
	}
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.methods)
}

// writes is every request that was not a read.
func (c *counter) writes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(c.methods), func(m string) bool { return m == http.MethodGet })
}

func TestSendMessageThroughASession(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/A/messages/1","thread":{"name":"spaces/A/threads/T"}}`)
	})
	var out SendMessageOutput
	call(t, cs, "send_message", map[string]any{"space_id": "spaces/A", "text": "hello"}, &out)
	if out.MessageID == nil || *out.MessageID != "spaces/A/messages/1" {
		t.Errorf("message id = %v", out.MessageID)
	}
	if out.ThreadID == nil || *out.ThreadID != "spaces/A/threads/T" {
		t.Errorf("thread id = %v", out.ThreadID)
	}
	if out.DryRun || out.RenderedPayload != nil {
		t.Errorf("a real post reported a preview: %+v", out)
	}
}

// A dry run writes nothing and shows the body a real post would send.
// One test per tool, because a tool that quietly posted anyway is the
// failure this flag exists to rule out. Most make no request at all.
// The ones marked reads look first, because the call would be refused
// on what they read: a delete's witness, an invite from outside the
// organization. Those may read and must not write.
func TestEveryDryRunWritesNothing(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range []struct {
		tool  string
		args  map[string]any
		reads bool
	}{
		{"send_message", map[string]any{"space_id": "spaces/A", "text": "hello"}, false},
		{"update_message", map[string]any{"message_name": "spaces/A/messages/1", "text": "edited"}, false},
		{"delete_message", map[string]any{"message_name": "spaces/A/messages/1", "text_start": "the standup moved to 10"}, true},
		{"delete_thread", map[string]any{"message_name": "spaces/A/messages/1", "text_start": "the standup moved to 10"}, true},
		{"create_group_chat", map[string]any{"member_emails": []string{"janedoe@example.com", "johndoe@example.com"}}, false},
		{"create_space", map[string]any{"display_name": "Team", "member_emails": []string{"janedoe@example.com"}}, false},
		{"update_space", map[string]any{"space_id": "spaces/A", "display_name": "Renamed"}, false},
		{"add_member", map[string]any{"space_id": "spaces/A", "user_email": "janedoe@example.com"}, true},
		{"add_external_member", map[string]any{"space_id": "spaces/A", "user_email": "guest@example.org"}, false},
		{"remove_member", map[string]any{"membership_name": "spaces/A/members/M"}, false},
		{"create_section", map[string]any{"display_name": "Clients"}, false},
		{"rename_section", map[string]any{"section_name": "users/me/sections/S", "display_name": "Clients"}, false},
		{"delete_section", map[string]any{"section_name": "users/me/sections/S"}, false},
		{"position_section", map[string]any{"section_name": "users/me/sections/S", "relative_position": "START"}, false},
		{"update_member_role", map[string]any{"membership_name": "spaces/A/members/M", "role": "MEMBER"}, false},
		{"promote_member", map[string]any{"membership_name": "spaces/A/members/M", "role": "MANAGER"}, false},
		{"delete_space", map[string]any{"space_id": "spaces/A", "display_name": "Team standup"}, true},
		{"set_space_audience", map[string]any{"space_id": "spaces/A", "audience": "default", "display_name": "Team standup"}, true},
		{"pin_message", map[string]any{"message_name": "spaces/A/messages/1"}, false},
		{"unpin_message", map[string]any{"message_name": "spaces/A/messages/1"}, false},
		{"mark_space_read", map[string]any{"space_id": "spaces/A"}, false},
		{"mark_space_unread", map[string]any{"space_id": "spaces/A", "from_time": "2026-01-02T03:04:05Z"}, false},
		{"update_space_notification_setting", map[string]any{"space_id": "spaces/A", "mute_setting": "MUTED"}, false},
		{"set_availability", map[string]any{"state": "AWAY"}, false},
		{"set_custom_status", map[string]any{"text": "in a workshop", "emoji": "🎉"}, false},
		{"create_custom_emoji", map[string]any{"emoji_name": ":team-flag:", "image_path": "emoji.png"}, false},
		{"delete_custom_emoji", map[string]any{"name": "customEmojis/AAAAemoji1", "emoji_name": ":test-emoji:"}, true},
		{"upload_attachment", map[string]any{"space_id": "spaces/A", "local_path": "emoji.png"}, false},
	} {
		covered[tc.tool] = true
		t.Run(tc.tool, func(t *testing.T) {
			c := &counter{}
			cs := session(t, c.wrap(everyEndpoint))
			args := map[string]any{"dry_run": true}
			for k, v := range tc.args {
				args[k] = v
			}
			res := call(t, cs, tc.tool, args, nil)
			if res.IsError {
				t.Fatalf("%s: %s", tc.tool, errorText(t, res))
			}
			if n := c.count(); !tc.reads && n != 0 {
				t.Errorf("a dry run made %d requests", n)
			}
			if tc.reads && c.count() == 0 {
				t.Error("a dry run that checks what it reads read nothing")
			}
			if w := c.writes(); len(w) != 0 {
				t.Errorf("a dry run wrote: %v", w)
			}
			var out struct {
				DryRun bool `json:"dry_run"`
			}
			raw, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if !out.DryRun {
				t.Error("dry_run is false in the result of a dry run")
			}
		})
	}
	// The list above is checked against what is registered, so a write
	// tool added tomorrow cannot go without a row. The move reads the
	// space's current section to show what it would do, and
	// TestTheMoveDryRunReadsButDoesNotWrite holds it instead.
	covered["move_space_to_section"] = true
	for _, name := range dryRunTools(t) {
		if !covered[name] {
			t.Errorf("%s takes dry_run and has no row here", name)
		}
	}
}

// move_space_to_section's preview says which section the space would
// leave, and that has to be looked up, so its dry run reads, in a shape
// the table's stub does not answer. What it must not do is write.
func TestTheMoveDryRunReadsButDoesNotWrite(t *testing.T) {
	c := &counter{}
	cs := session(t, c.wrap(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"sectionItems":[{"name":"users/123/sections/S/items/III","space":"spaces/A"}]}`)
	}))
	var out MoveSpaceToSectionOutput
	call(t, cs, "move_space_to_section", map[string]any{
		"space_id": "spaces/A", "section_name": "users/123/sections/T", "dry_run": true,
	}, &out)
	if out.Moved || out.FromSection != "users/123/sections/S" {
		t.Errorf("result = %+v", out)
	}
	if w := c.writes(); len(w) != 0 {
		t.Errorf("a dry run wrote: %v", w)
	}
}

// Deleting something that is already gone is the state the caller asked
// for, so it comes back as a result rather than an error.
func TestDeleteMessageReportsAnAlreadyGoneMessage(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"status":"NOT_FOUND","message":"gone"}}`)
	})
	var out DeleteMessageOutput
	res := call(t, cs, "delete_message", map[string]any{
		"message_name": "spaces/A/messages/1", "text_start": "the standup moved", "confirm": true,
	}, &out)
	if res.IsError {
		t.Fatalf("a repeat delete should not fail: %s", errorText(t, res))
	}
	if out.Deleted {
		t.Error("deleted = true for a message that was already gone")
	}
}

// A group in a space that admits people from outside goes through
// add_external_member, which takes group_name and the group's address;
// add_member refuses it.
func TestAGroupWhereOutsidersAreAdmittedIsAddedOnlyAsExternal(t *testing.T) {
	var sent []map[string]any
	cs := session(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cloudidentity/") {
			fmt.Fprint(w, `{"name":"groups/AAAAgroup1","groupKey":{"id":"team@example.com"}}`)
			return
		}
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"name":"spaces/A","spaceType":"SPACE","externalUserAllowed":true}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, body)
		fmt.Fprint(w, `{"name":"spaces/A/members/M"}`)
	})
	args := map[string]any{"space_id": "spaces/A", "group_name": "groups/AAAAgroup1"}
	res := call(t, cs, "add_member", args, nil)
	if !res.IsError || !strings.HasPrefix(errorText(t, res), "[blocked]") {
		t.Fatalf("add_member added a group to a space that admits outsiders: %+v", res)
	}
	args["confirm"], args["group_email"] = true, "team@example.com"
	var out AddMemberOutput
	if res := call(t, cs, "add_external_member", args, &out); res.IsError {
		t.Fatalf("add_external_member: %s", errorText(t, res))
	}
	if len(sent) != 1 {
		t.Fatalf("%d writes, want the one group add", len(sent))
	}
	group, _ := sent[0]["groupMember"].(map[string]any)
	if group["name"] != "groups/AAAAgroup1" || out.GroupName != "groups/AAAAgroup1" {
		t.Errorf("sent %v and reported %+v, want groups/AAAAgroup1 added", sent[0], out)
	}
}

// A misspelled dry_run must fail rather than post for real, which is
// what additionalProperties false buys.
func TestAMisspelledArgumentIsRefused(t *testing.T) {
	c := &counter{}
	cs := session(t, c.wrap(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/A/messages/1"}`)
	}))
	res := call(t, cs, "send_message", map[string]any{
		"space_id": "spaces/A", "text": "hello", "dryrun": true,
	}, nil)
	if !res.IsError {
		t.Fatal("an unknown argument was accepted")
	}
	if c.count() != 0 {
		t.Errorf("the message was posted anyway (%d requests)", c.count())
	}
}

// The two ways of saying where a section goes are a union upstream, so
// both together has to be refused before it reaches Google.
func TestPositionSectionRefusesBothWaysAtOnce(t *testing.T) {
	c := &counter{}
	cs := session(t, c.wrap(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) }))
	res := call(t, cs, "position_section", map[string]any{
		"section_name": "users/me/sections/S", "sort_order": 2, "relative_position": "START",
	}, nil)
	if text := errorText(t, res); !strings.HasPrefix(text, "[invalid]") {
		t.Errorf("error = %q", text)
	}
	if c.count() != 0 {
		t.Errorf("the request went to Google anyway (%d requests)", c.count())
	}
}

// registeredNames lists what Register puts on a server under cfg.
func registeredNames(t *testing.T, cfg config.Config) []string {
	t.Helper()
	return toolNames(t, sessionWithConfig(t, func(http.ResponseWriter, *http.Request) {}, cfg))
}

// GCM_READ_ONLY is the flag a person sets when they want this server
// nowhere near their Chat history. The wrapper enforces it, and this
// checks the whole registered surface rather than the wrapper alone: a
// tool declared with the wrong kind would pass that test and fail this
// one.
//
// download_attachment is the one tool here that is not in readTools:
// it reads Chat and writes a file, so it is not marked read-only, and
// GCM_READ_ONLY still registers it because that flag is about Chat.
// get_space_notification_setting is the one read left out: Google takes
// it only through a scope that can also write, which read-only mode does
// not ask for.
func TestReadOnlyRegistersExactlyTheReadTools(t *testing.T) {
	want := append([]string{"download_attachment"}, readTools...)
	want = slices.DeleteFunc(want, func(name string) bool { return name == "get_space_notification_setting" })
	slices.Sort(want)
	cfg := config.Config{Toolsets: config.AllToolsets, ReadOnly: true}
	if got := registeredNames(t, cfg); !slices.Equal(got, want) {
		t.Errorf("read-only registered\n%v\nwant\n%v", got, want)
	}
}

// find_direct_message is not read-only, unlike its name: it creates the
// space when there is none, so read-only mode has to leave it out.
func TestFindDirectMessageCountsAsAWrite(t *testing.T) {
	full := registeredNames(t, config.Config{Toolsets: config.AllToolsets})
	if !slices.Contains(full, "find_direct_message") {
		t.Fatal("find_direct_message is not registered by default")
	}
	readOnly := registeredNames(t, config.Config{Toolsets: config.AllToolsets, ReadOnly: true})
	if slices.Contains(readOnly, "find_direct_message") {
		t.Error("find_direct_message survives read-only mode, but it can create a space")
	}
}

// Every write but the destructive ones is registered by default:
// read-only is opt-in, and GCM_ENABLE_DESTRUCTIVE adds the rest.
// internal/server holds the whole baseline against the schema dump;
// this holds the split here, where a tool is registered.
func TestTheWholeSurfaceIsRegisteredByDefault(t *testing.T) {
	destructive := []string{
		"add_external_member", "delete_custom_emoji", "delete_message", "delete_section", "delete_space",
		"delete_thread", "promote_member", "remove_member", "set_space_audience",
	}
	got := registeredNames(t, config.Config{Toolsets: config.AllToolsets})
	for _, name := range []string{"send_message", "add_member", "move_space_to_section", "update_space", "unpin_message", "remove_reaction"} {
		if !slices.Contains(got, name) {
			t.Errorf("%q is not registered by default", name)
		}
	}
	for _, name := range destructive {
		if slices.Contains(got, name) {
			t.Errorf("%q is registered without GCM_ENABLE_DESTRUCTIVE", name)
		}
	}
	all := registeredNames(t, config.Config{}.FullSurface())
	if want := len(got) + len(destructive); len(all) != want {
		t.Errorf("%d tools with GCM_ENABLE_DESTRUCTIVE, want %d: the default surface and the %d destructive tools",
			len(all), want, len(destructive))
	}
	for _, name := range destructive {
		if !slices.Contains(all, name) {
			t.Errorf("%q is missing with GCM_ENABLE_DESTRUCTIVE", name)
		}
	}
}

// Claude Code asks on every call for exactly the tools whose prompt is
// itself the point: deleting a whole space or an emoji every message
// shares, and opening a space to an audience. Every other write is the
// client's to approve, sending and deleting a message included, since a
// mark there would ask once per message of a bulk delete.
func TestOnlyTheMarkedSetAsksForAPerson(t *testing.T) {
	cs := session(t, func(http.ResponseWriter, *http.Request) {})
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var marked []string
	for _, tool := range list.Tools {
		if _, ok := tool.Meta["anthropic/requiresUserInteraction"]; ok {
			marked = append(marked, tool.Name)
		}
	}
	slices.Sort(marked)
	if want := []string{"delete_custom_emoji", "delete_space", "set_space_audience"}; !slices.Equal(marked, want) {
		t.Errorf("marked %v, want %v", marked, want)
	}
}

// dryRunTools is every registered tool whose input takes dry_run.
func dryRunTools(t *testing.T) []string {
	t.Helper()
	cs := session(t, body(`{}`))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var out []string
	for _, tool := range list.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s: %v", tool.Name, err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("decode %s: %v", tool.Name, err)
		}
		if _, ok := schema.Properties["dry_run"]; ok {
			out = append(out, tool.Name)
		}
	}
	if len(out) < 20 {
		t.Fatalf("found %d tools taking dry_run, below the floor of 20: %v", len(out), out)
	}
	return out
}
