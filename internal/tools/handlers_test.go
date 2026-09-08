package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/directory"
	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// session registers the tools against a stub Google and returns a
// connected client. Calls go over a real MCP session, so a handler is
// exercised the way a client exercises it: input decoded from JSON,
// output encoded back.
func session(t *testing.T, handler http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	return sessionWithToolsets(t, handler, "")
}

// sessionWithToolsets is session with a narrowed tool surface. An empty
// list registers everything.
func sessionWithToolsets(t *testing.T, handler http.HandlerFunc, toolsets ...config.Toolset) *mcp.ClientSession {
	t.Helper()
	// The default surface, not every named set: admin is off unless a
	// person names it, and a harness that quietly turned it on would
	// test a configuration nobody runs.
	// A local directory per test, so the file-transfer tools are on.
	// Leaving it unset would make every one of them refuse, and a
	// harness that could not drive them would leave them untested.
	cfg := config.Config{Toolsets: config.DefaultToolsets(), LocalDir: localDir(t)}
	if len(toolsets) > 0 && toolsets[0] != "" {
		cfg.Toolsets = toolsets
	}
	return sessionWithConfig(t, handler, cfg)
}

// sessionWithConfig is the whole harness: a stub Google, the service
// wired to it, and a connected client. Every test in this package goes
// through here, so there is one wiring to keep up to date.
func sessionWithConfig(t *testing.T, handler http.HandlerFunc, cfg config.Config) *mcp.ClientSession {
	t.Helper()
	return sessionWithLogger(t, handler, cfg, slog.New(slog.DiscardHandler))
}

// sessionWithLogger is sessionWithConfig with the logs kept. Only the
// logging test needs them; everything else discards.
func sessionWithLogger(t *testing.T, handler http.HandlerFunc, cfg config.Config, log *slog.Logger) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := gchat.New(gchat.Options{
		HTTP:       srv.Client(),
		ChatBase:   srv.URL + "/v1",
		PeopleBase: srv.URL + "/people",
		OIDCBase:   srv.URL + "/oidc",
		Tokens:     staticToken(canaryToken),
		Logger:     log,
		// Unthrottled. The per-user limits are gchat's concern and are
		// tested there; here they would only make a suite that drives
		// every write tool wait out the one-per-second write rate.
		ReadLimiter:   rate.NewLimiter(rate.Inf, 1),
		PeopleLimiter: rate.NewLimiter(rate.Inf, 1),
		WriteLimiter:  rate.NewLimiter(rate.Inf, 1),
	})
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	Register(s, Deps{
		Service: service.New(client, directory.NewResolver(client, directory.NewCache("", time.Hour, log), log), cfg, log),
		Config:  cfg,
		Logger:  log,
	})

	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call runs one tool and decodes its structured result.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if !res.IsError {
		bothHalves(t, name, res)
	}
	if out != nil && !res.IsError {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode result: %v", err)
		}
	}
	return res
}

// bothHalves is the rendering rule applied to every tool the suite
// calls, rather than once per tool: a reply carries the structured half
// and a readable one, and the readable one is not the JSON again. See
// render.go.
func bothHalves(t *testing.T, name string, res *mcp.CallToolResult) {
	t.Helper()
	if res.StructuredContent == nil {
		t.Errorf("%s: no structured content; the output schema promises one", name)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: %d content blocks, want exactly the rendered text", name, len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s: content block is %T, want text", name, res.Content[0])
	}
	if strings.TrimSpace(tc.Text) == "" {
		t.Errorf("%s: the readable half is empty", name)
	}
	if strings.HasPrefix(strings.TrimSpace(tc.Text), "{") {
		t.Errorf("%s: the readable half is serialized JSON, which is the duplication it replaces: %s", name, tc.Text)
	}
}

// errorText is the text a failing tool call shows the model.
func errorText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatal("expected a tool error")
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestWhoamiThroughASession(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"sub":"12345","email":"janedoe@example.com","name":"Jane Doe","hd":"example.com"}`)
	})
	var out WhoamiOutput
	call(t, cs, "whoami", nil, &out)
	if out.UserSub != "12345" || out.Email != "janedoe@example.com" {
		t.Errorf("whoami = %+v", out)
	}
}

func TestListSpacesThroughASession(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[
		  {"name":"spaces/A","spaceType":"SPACE","displayName":"Team","threaded":true},
		  {"name":"spaces/B","spaceType":"DIRECT_MESSAGE"}
		],"nextPageToken":"next"}`)
	})
	var out ListSpacesOutput
	call(t, cs, "list_spaces", map[string]any{"limit": 2}, &out)
	if len(out.Result) != 2 {
		t.Fatalf("list_spaces = %+v", out)
	}
	if out.Result[0].Type != "SPACE" || out.Result[1].Type != "DIRECT_MESSAGE" {
		t.Errorf("types = %q, %q", out.Result[0].Type, out.Result[1].Type)
	}
	if out.Result[1].DisplayName == "" {
		t.Error("a direct message must still have something to call it")
	}
	if out.Result[0].SpaceID != "spaces/A" {
		t.Errorf("space id = %q", out.Result[0].SpaceID)
	}
}

// A failure from Google arrives as a tool error with its class, not as
// a JSON-RPC error: the protocol reserves those for the caller getting
// the request wrong.
func TestAGoogleRefusalBecomesAToolError(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
	})
	res := call(t, cs, "list_spaces", nil, nil)
	text := errorText(t, res)
	if !strings.HasPrefix(text, "[scope]") {
		t.Errorf("error = %q, want the scope class", text)
	}
	if !strings.Contains(text, "googleapis.com/auth/") {
		t.Errorf("error = %q, want it to name the scope URL", text)
	}
}

// Bad arguments are the caller's fault and must be caught before any
// request reaches Google.
func TestBadArgumentsNeverReachGoogle(t *testing.T) {
	var reached bool
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		fmt.Fprint(w, `{"spaces":[]}`)
	})
	res := call(t, cs, "list_spaces", map[string]any{"space_type": "CHANNEL"}, nil)
	if text := errorText(t, res); !strings.HasPrefix(text, "[invalid]") {
		t.Errorf("error = %q, want the invalid class", text)
	}
	if reached {
		t.Error("a request went to Google despite invalid arguments")
	}
}

// localDir is a directory the file-transfer tools may use, holding the
// one file the table above names. The tools refuse a path outside it,
// so the fixture has to be copied in rather than pointed at.
func localDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	image, err := os.ReadFile(filepath.Join("testdata", "emoji.png"))
	if err != nil {
		t.Fatalf("read the fixture image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "emoji.png"), image, 0o600); err != nil {
		t.Fatalf("write the fixture image: %v", err)
	}
	return dir
}

// toolArgs is one valid argument set per registered tool.
//
// It lives here rather than in either test that uses it, because both
// need the same thing: a way to drive the whole surface and fail when a
// tool has no entry. Without that, a tool added later is silently
// exempt from the rules those tests enforce — which is how the logging
// test came to cover eight tools out of twenty-eight.
var toolArgs = map[string]map[string]any{
	"whoami":                            {},
	"list_spaces":                       {},
	"search_spaces":                     {"display_name": "Engineering"},
	"download_attachment":               {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1"},
	"upload_attachment":                 {"space_id": "spaces/AAAAspace1", "local_path": "emoji.png"},
	"list_space_events":                 {"space_id": "spaces/AAAAspace1", "event_types": []any{"message_created"}},
	"get_space_event":                   {"event_name": "spaces/AAAAspace1/spaceEvents/AAAAevent1"},
	"get_member":                        {"membership_name": "spaces/AAAAspace1/members/AAAAmember1"},
	"list_pinned_messages":              {"space_id": "spaces/AAAAspace1"},
	"get_availability":                  {},
	"set_availability":                  {"state": "AWAY", "dry_run": true},
	"set_custom_status":                 {"text": "in a workshop", "emoji": "🛠", "dry_run": true},
	"list_custom_emojis":                {},
	"get_custom_emoji":                  {"name": "customEmojis/AAAAemoji1"},
	"delete_custom_emoji":               {"name": "customEmojis/AAAAemoji1", "dry_run": true},
	"create_custom_emoji":               {"emoji_name": ":test-emoji:", "image_path": "emoji.png", "dry_run": true},
	"delete_space":                      {"space_id": "spaces/AAAAspace1", "confirm_space_id": "spaces/AAAAspace1", "dry_run": true},
	"pin_message":                       {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1", "dry_run": true},
	"unpin_message":                     {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1", "dry_run": true},
	"get_space_read_state":              {"space_id": "spaces/AAAAspace1"},
	"get_thread_read_state":             {"space_id": "spaces/AAAAspace1", "thread_name": "spaces/AAAAspace1/threads/AAAAthread1"},
	"mark_space_read":                   {"space_id": "spaces/AAAAspace1", "dry_run": true},
	"mark_space_unread":                 {"space_id": "spaces/AAAAspace1", "from_time": "2026-01-02T03:04:05Z", "dry_run": true},
	"get_space_notification_setting":    {"space_id": "spaces/AAAAspace1"},
	"update_space_notification_setting": {"space_id": "spaces/AAAAspace1", "mute_setting": "MUTED", "dry_run": true},
	"update_member_role":                {"membership_name": "spaces/AAAAspace1/members/AAAAmember1", "role": "MANAGER", "dry_run": true},
	"find_group_chats":                  {"member_emails": []any{"janedoe@example.com"}},
	"get_space":                         {"space_id": "spaces/AAAAspace1"},
	"find_direct_message":               {"user_email": "janedoe@example.com"},
	"get_messages":                      {"space_id": "spaces/AAAAspace1"},
	"get_message":                       {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1"},
	"get_thread":                        {"space_id": "spaces/AAAAspace1", "thread_name": "spaces/AAAAspace1/threads/AAAAthread1"},
	"search_messages":                   {"space_id": "spaces/AAAAspace1", "query": "standup"},
	"search_people":                     {"query": "jane"},
	"list_members":                      {"space_id": "spaces/AAAAspace1"},
	"list_reactions":                    {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1"},
	"list_sections":                     {},
	"list_section_items":                {"section_name": "users/me/sections/AAAAsection1"},
	"send_message":                      {"space_id": "spaces/AAAAspace1", "text": "hello"},
	"update_message":                    {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1", "text": "edited"},
	"delete_message":                    {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1"},
	"add_reaction":                      {"message_name": "spaces/AAAAspace1/messages/AAAAmsg1", "emoji": "👍"},
	"remove_reaction":                   {"reaction_name": "spaces/AAAAspace1/messages/AAAAmsg1/reactions/AAAAreact1"},
	"create_group_chat":                 {"member_emails": []string{"janedoe@example.com", "johndoe@example.com"}},
	"create_space":                      {"display_name": "Team standup", "member_emails": []string{"janedoe@example.com"}},
	"update_space":                      {"space_id": "spaces/AAAAspace1", "display_name": "Renamed"},
	"add_member":                        {"space_id": "spaces/AAAAspace1", "user_email": "janedoe@example.com"},
	"remove_member":                     {"membership_name": "spaces/AAAAspace1/members/AAAAmember1"},
	"create_section":                    {"display_name": "Clients"},
	"rename_section":                    {"section_name": "users/me/sections/AAAAsection1", "display_name": "Clients"},
	"delete_section":                    {"section_name": "users/me/sections/AAAAsection1"},
	"position_section":                  {"section_name": "users/me/sections/AAAAsection1", "relative_position": "START"},
	"move_space_to_section":             {"space_id": "spaces/AAAAspace1", "section_name": "users/me/sections/AAAAsection1", "item_name": "users/me/sections/AAAAsection1/items/AAAAitem1"},
}

// everyRegisteredTool checks the table above still covers the surface,
// and returns the tools to drive.
func everyRegisteredTool(t *testing.T, cs *mcp.ClientSession) map[string]map[string]any {
	t.Helper()
	registered, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range registered.Tools {
		if _, ok := toolArgs[tool.Name]; !ok {
			t.Errorf("%s has no entry in toolArgs, so nothing drives it here", tool.Name)
		}
	}
	if len(toolArgs) != len(registered.Tools) {
		t.Errorf("toolArgs has %d entries for %d registered tools", len(toolArgs), len(registered.Tools))
	}
	return toolArgs
}
