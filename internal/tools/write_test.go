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

	"github.com/mmedum/google-chat-mcp/internal/config"
)

// counter records how many requests a tool call made. A dry run's whole
// promise is that the answer is zero.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
		h(w, r)
	}
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
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

// A dry run makes no request and shows the body a real post would send.
// One test per tool, because a tool that quietly posted anyway is the
// failure this flag exists to rule out.
func TestEveryDryRunReachesNothing(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"send_message", map[string]any{"space_id": "spaces/A", "text": "hello"}},
		{"update_message", map[string]any{"message_name": "spaces/A/messages/1", "text": "edited"}},
		{"delete_message", map[string]any{"message_name": "spaces/A/messages/1"}},
		{"create_group_chat", map[string]any{"member_emails": []string{"janedoe@example.com", "johndoe@example.com"}}},
		{"create_space", map[string]any{"display_name": "Team", "member_emails": []string{"janedoe@example.com"}}},
		{"update_space", map[string]any{"space_id": "spaces/A", "display_name": "Renamed"}},
		{"add_member", map[string]any{"space_id": "spaces/A", "user_email": "janedoe@example.com"}},
		{"remove_member", map[string]any{"membership_name": "spaces/A/members/M"}},
		{"create_section", map[string]any{"display_name": "Clients"}},
		{"rename_section", map[string]any{"section_name": "users/me/sections/S", "display_name": "Clients"}},
		{"delete_section", map[string]any{"section_name": "users/me/sections/S"}},
		{"position_section", map[string]any{"section_name": "users/me/sections/S", "relative_position": "START"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			c := &counter{}
			cs := session(t, c.wrap(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"name":"spaces/A/messages/1"}`)
			}))
			args := map[string]any{"dry_run": true}
			for k, v := range tc.args {
				args[k] = v
			}
			res := call(t, cs, tc.tool, args, nil)
			if res.IsError {
				t.Fatalf("%s: %s", tc.tool, errorText(t, res))
			}
			if c.count() != 0 {
				t.Errorf("a dry run made %d requests", c.count())
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
}

// move_space_to_section is the one dry run that reads: the preview says
// which section the space would leave, and that has to be looked up.
// What it must not do is write.
func TestTheMoveDryRunReadsButDoesNotWrite(t *testing.T) {
	var methods []string
	var mu sync.Mutex
	cs := session(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		fmt.Fprint(w, `{"sectionItems":[{"name":"users/123/sections/S/items/III","space":"spaces/A"}]}`)
	})
	var out MoveSpaceToSectionOutput
	call(t, cs, "move_space_to_section", map[string]any{
		"space_id": "spaces/A", "section_name": "users/123/sections/T", "dry_run": true,
	}, &out)
	if out.Moved || out.FromSection != "users/123/sections/S" {
		t.Errorf("result = %+v", out)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range methods {
		if m != "GET" {
			t.Errorf("a dry run made a %s request", m)
		}
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
	res := call(t, cs, "delete_message", map[string]any{"message_name": "spaces/A/messages/1"}, &out)
	if res.IsError {
		t.Fatalf("a repeat delete should not fail: %s", errorText(t, res))
	}
	if out.Deleted {
		t.Error("deleted = true for a message that was already gone")
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
	cs := sessionWithConfig(t, func(http.ResponseWriter, *http.Request) {}, cfg)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
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
func TestReadOnlyRegistersExactlyTheReadTools(t *testing.T) {
	want := append([]string{"download_attachment"}, readTools...)
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

// Every write is registered by default: read-only is opt-in, and the
// released surface is a floor a change may add to, never drop from.
// internal/server holds the whole baseline against the schema dump;
// this holds the count here, where a tool is registered.
func TestTheWholeSurfaceIsRegisteredByDefault(t *testing.T) {
	got := registeredNames(t, config.Config{Toolsets: config.AllToolsets})
	if len(got) < 53 {
		t.Errorf("%d tools registered, fewer than the 53 of the released surface: %v", len(got), got)
	}
	for _, name := range []string{"send_message", "delete_message", "move_space_to_section", "update_space"} {
		if !slices.Contains(got, name) {
			t.Errorf("%q is not registered", name)
		}
	}
}

// The tools a person would want to be asked about are the ones whose
// effect other people see and nobody can undo. delete_section is the
// counter-example that makes the point: it is destructive, but it is
// the caller's own sidebar.
func TestTheOutwardFacingToolsAskForAPerson(t *testing.T) {
	cs := session(t, func(http.ResponseWriter, *http.Request) {})
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	asks := map[string]bool{}
	for _, tool := range list.Tools {
		_, ok := tool.Meta["anthropic/requiresUserInteraction"]
		asks[tool.Name] = ok
	}
	for _, name := range []string{
		"send_message", "update_message", "delete_message",
		"add_member", "remove_member", "create_space", "create_group_chat", "update_space",
	} {
		if !asks[name] {
			t.Errorf("%q writes where other people can see it and asks for nobody", name)
		}
	}
	for _, name := range []string{"list_spaces", "get_messages", "add_reaction", "find_direct_message"} {
		if asks[name] {
			t.Errorf("%q demands an interaction it does not need", name)
		}
	}
}
