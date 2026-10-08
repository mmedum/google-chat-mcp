package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/v4/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The protocols a question goes out on: before 2026-07-28 the SDK asks
// with elicitation/create inside the call; from it, the call returns the
// question and comes back with the answer.
var protocols = []string{"2025-06-18", "2025-11-25", "2026-07-28"}

// answerer answers the questions a test client is asked, and keeps them.
type answerer struct {
	mu        sync.Mutex
	questions []*mcp.ElicitParams
	answer    func() (*mcp.ElicitResult, error)
}

func (p *answerer) handle(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	p.mu.Lock()
	p.questions = append(p.questions, req.Params)
	answer := p.answer
	p.mu.Unlock()
	return answer()
}

func (p *answerer) asked() []*mcp.ElicitParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.questions)
}

func says(action string) func() (*mcp.ElicitResult, error) {
	return func() (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: action}, nil }
}

var (
	accepts  = says("accept")
	declines = says("decline")
)

// stubChat is a Google that answers every read a question needs and
// counts every write.
type stubChat struct {
	mu     sync.Mutex
	writes int
}

func (g *stubChat) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		g.mu.Lock()
		g.writes++
		g.mu.Unlock()
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodDelete:
		fmt.Fprint(w, `{}`)
	case strings.Contains(path, "/customEmojis/"):
		fmt.Fprint(w, `{"name":"customEmojis/AAAAemoji1","emojiName":":party-*time*:"}`)
	case strings.Contains(path, "/messages/"):
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1","sender":{"name":"users/1","displayName":"Ada [Lovelace](x)"},`+
			`"createTime":"2026-01-02T03:04:05Z","text":"The **plan** is at https://evil.example/login"}`)
	case strings.HasSuffix(path, "/members"):
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/members/AAAAmember1","role":"ROLE_MEMBER"}`)
	case strings.HasSuffix(path, "/messages"):
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg2"}`)
	default:
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1","displayName":"Team *room*","spaceType":"SPACE"}`)
	}
}

func (g *stubChat) written() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writes
}

// askOptions adjust an asking session.
type askOptions struct {
	cfg        *config.Config
	client     func(*mcp.ClientOptions)
	middleware []mcp.Middleware
}

// askingSession connects a client that declares form elicitation and answers
// with p, on protocol, to every tool with deletes allowed.
func askingSession(t *testing.T, protocol string, p *answerer, o askOptions) (*mcp.ClientSession, *stubChat) {
	t.Helper()
	g := &stubChat{}
	cfg := config.Config{Toolsets: config.AllToolsets, LocalDir: localDir(t)}
	if o.cfg != nil {
		cfg = *o.cfg
	}
	co := &mcp.ClientOptions{}
	if p != nil {
		co.ElicitationHandler = p.handle
	}
	if o.client != nil {
		o.client(co)
	}
	cs := connectClient(t, g.handler, cfg, slog.New(slog.DiscardHandler), co, protocol, o.middleware...)
	return cs, g
}

// askCases are arguments that reach each asking tool's write, with
// words its question must carry.
var askCases = map[string]struct {
	args  map[string]any
	shows []string
}{
	"delete_message": {
		args:  map[string]any{"message_name": "spaces/AAAAspace1/messages/AAAAmsg1"},
		shows: []string{"delete a message by `Ada [Lovelace](x)` in `spaces/AAAAspace1` for good?", "text: `The **plan** is at https[:]//evil[.]example/login`"},
	},
	"delete_space": {
		args:  map[string]any{"space_id": "spaces/AAAAspace1", "confirm_space_id": "spaces/AAAAspace1"},
		shows: []string{"delete the space `Team *room*`, `spaces/AAAAspace1` for good?", "for everyone in it"},
	},
	"delete_custom_emoji": {
		args:  map[string]any{"name": "customEmojis/AAAAemoji1"},
		shows: []string{"delete the custom emoji `:party-*time*:`", "for everyone in the organization"},
	},
	"add_member": {
		args:  map[string]any{"space_id": "spaces/AAAAspace1", "user_email": "janedoe@example.com"},
		shows: []string{"add the person `janedoe@example.com` to the space `Team *room*`"},
	},
	"update_space": {
		args: map[string]any{"space_id": "spaces/AAAAspace1", "audience": "default"},
		shows: []string{"open the space `Team *room*`, `spaces/AAAAspace1` to the organization's default target audience, `audiences/default`?",
			"Making it private again does not take back what they read."},
	},
	"create_space": {
		args:  map[string]any{"display_name": "Launch", "audience": "audiences/AAAAaudience1"},
		shows: []string{"create the space `Launch` open to the target audience `audiences/AAAAaudience1`?", "join without an invitation"},
	},
	"update_message": {
		args:  map[string]any{"message_name": "spaces/AAAAspace1/messages/AAAAmsg1", "text": "Now for <users/all>"},
		shows: []string{"so it mentions everyone in the space?", "new text: `Now for <users/all>`"},
	},
	"send_message": {
		args:  map[string]any{"space_id": "spaces/AAAAspace1", "text": "Standup moved <users/all>"},
		shows: []string{"mentioning everyone in it?", "text: `Standup moved <users/all>`", "Everyone in the space is notified"},
	},
}

// asksPerson is every tool registered to ask, read from what each
// published description says.
func asksPerson(t *testing.T) []string {
	t.Helper()
	cs, _ := askingSession(t, "", nil, askOptions{})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tool := range res.Tools {
		if strings.Contains(tool.Description, "the server also asks the person") {
			out = append(out, tool.Name)
		}
	}
	return out
}

// Every tool that asks puts one question to the person when the client
// can ask: declined, it writes nothing; accepted, it writes. The list
// comes from the published descriptions, with a floor.
func TestEveryAskingToolAsksThePerson(t *testing.T) {
	names := asksPerson(t)
	if len(names) < 6 {
		t.Fatalf("found %d tools that ask, below the floor of 6: %v", len(names), names)
	}
	for _, name := range names {
		c, ok := askCases[name]
		if !ok {
			t.Errorf("%s asks the person and has no case here: add one", name)
			continue
		}
		for _, protocol := range protocols {
			t.Run(name+"/"+protocol, func(t *testing.T) {
				p := &answerer{answer: declines}
				cs, g := askingSession(t, protocol, p, askOptions{})
				res := callRaw(t, cs, &mcp.CallToolParams{Name: name, Arguments: c.args})
				if text := textOf(res); !res.IsError || !strings.HasPrefix(text, "[blocked]") ||
					!strings.Contains(text, "not confirmed by the person") || strings.Contains(text, "declined") {
					t.Errorf("refusal: %s", text)
				}
				if g.written() != 0 {
					t.Fatalf("declined, and %d writes were sent", g.written())
				}
				qs := p.asked()
				if len(qs) != 1 {
					t.Fatalf("asked %d questions", len(qs))
				}
				if qs[0].Mode != "form" || !strings.HasPrefix(qs[0].Message, name+": ") || !isEmptyForm(qs[0].RequestedSchema) {
					t.Errorf("question %+v", qs[0])
				}
				for _, s := range c.shows {
					if !strings.Contains(qs[0].Message, s) {
						t.Errorf("the question does not show %q:\n%s", s, qs[0].Message)
					}
				}

				p.answer = accepts
				if res := callRaw(t, cs, &mcp.CallToolParams{Name: name, Arguments: c.args}); res.IsError {
					t.Fatalf("accepted, and refused: %s", textOf(res))
				}
				if g.written() != 1 {
					t.Fatalf("accepted, and %d writes were sent", g.written())
				}
			})
		}
	}
}

// isEmptyForm says the question's form has no fields: accepting it is
// the confirmation.
func isEmptyForm(schema any) bool {
	s, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	props, ok := s["properties"].(map[string]any)
	return s["type"] == "object" && ok && len(props) == 0
}

func callRaw(t *testing.T, cs *mcp.ClientSession, p *mcp.CallToolParams) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), p)
	if err != nil {
		t.Fatalf("calling %s: %v", p.Name, err)
	}
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Only an accept confirms. Whatever else comes back, nothing is sent.
func TestOnlyAnAcceptConfirms(t *testing.T) {
	for _, protocol := range protocols {
		for _, tc := range []struct {
			name   string
			answer func() (*mcp.ElicitResult, error)
		}{
			{"decline", declines},
			{"cancel", says("cancel")},
			{"error", func() (*mcp.ElicitResult, error) { return nil, errors.New("no dialog here") }},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				cs, g := askingSession(t, protocol, &answerer{answer: tc.answer}, askOptions{})
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "delete_message", Arguments: askCases["delete_message"].args})
				if err != nil {
					// From 2026-07-28 the client fulfills the question itself, and
					// an answer it cannot give fails there.
					if protocol != "2026-07-28" {
						t.Fatalf("calling: %v", err)
					}
				} else if text := textOf(res); !res.IsError || !strings.HasPrefix(text, "[blocked]") || strings.Contains(text, "no dialog here") {
					t.Errorf("result: %s", text)
				}
				if g.written() != 0 {
					t.Fatalf("%d writes", g.written())
				}
			})
		}
	}
}

// A client that declares no elicitation gets no question, unless
// GCM_REQUIRE_PROMPT refuses what cannot be asked.
func TestNoPromptPossible(t *testing.T) {
	cs, g := askingSession(t, "", nil, askOptions{})
	callRaw(t, cs, &mcp.CallToolParams{Name: "delete_message", Arguments: askCases["delete_message"].args})
	if g.written() != 1 {
		t.Fatalf("%d writes", g.written())
	}
	strict := config.Config{Toolsets: config.AllToolsets, LocalDir: localDir(t), RequirePrompt: true}
	for _, name := range asksPerson(t) {
		cs, g := askingSession(t, "", nil, askOptions{cfg: &strict})
		res := callRaw(t, cs, &mcp.CallToolParams{Name: name, Arguments: askCases[name].args})
		if text := textOf(res); !res.IsError || !strings.Contains(text, "GCM_REQUIRE_PROMPT") || g.written() != 0 {
			t.Errorf("%s: %s; %d writes", name, text, g.written())
		}
	}
}

// A dry run never asks, a refused delete asks nobody, and a post that
// mentions no one asks nothing unless the configuration says every post
// asks.
func TestNothingIsAskedThatWouldNotBeWritten(t *testing.T) {
	p := &answerer{answer: accepts}
	cs, _ := askingSession(t, "2025-11-25", p, askOptions{})
	for name, c := range askCases {
		args := maps.Clone(c.args)
		args["dry_run"] = true
		if res := callRaw(t, cs, &mcp.CallToolParams{Name: name, Arguments: args}); res.IsError {
			t.Errorf("%s dry run: %s", name, textOf(res))
		}
	}
	callRaw(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"space_id": "spaces/AAAAspace1", "text": "hello"}})
	// Making a space private, renaming it, or creating one that stays
	// private takes nothing from anyone.
	callRaw(t, cs, &mcp.CallToolParams{Name: "update_space", Arguments: map[string]any{"space_id": "spaces/AAAAspace1", "audience": "private"}})
	callRaw(t, cs, &mcp.CallToolParams{Name: "update_space", Arguments: map[string]any{"space_id": "spaces/AAAAspace1", "display_name": "Team"}})
	callRaw(t, cs, &mcp.CallToolParams{Name: "create_space", Arguments: map[string]any{"display_name": "Launch"}})
	if n := len(p.asked()); n != 0 {
		t.Errorf("asked %d questions", n)
	}

	refusing := config.Config{Toolsets: config.AllToolsets, LocalDir: localDir(t), RefuseDeletes: true}
	cs, g := askingSession(t, "2025-11-25", p, askOptions{cfg: &refusing})
	callRaw(t, cs, &mcp.CallToolParams{Name: "delete_message", Arguments: askCases["delete_message"].args})
	if n := len(p.asked()); n != 0 || g.written() != 0 {
		t.Errorf("refused delete: asked %d, wrote %d", n, g.written())
	}

	every := config.Config{Toolsets: config.AllToolsets, LocalDir: localDir(t), AskBeforeSend: true}
	cs, _ = askingSession(t, "2025-11-25", p, askOptions{cfg: &every})
	callRaw(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"space_id": "spaces/AAAAspace1", "text": "hello"}})
	if qs := p.asked(); len(qs) != 1 || !strings.Contains(qs[0].Message, "send_message: post to the space `Team *room*`") ||
		strings.Contains(qs[0].Message, "everyone") {
		t.Errorf("GCM_ASK_BEFORE_SEND: %v", qs)
	}
}

// mrtr connects on 2026-07-28 with the client's own round trip off, so
// the test answers, forges and replays by hand.
func mrtr(t *testing.T) (*mcp.ClientSession, *stubChat) {
	t.Helper()
	return askingSession(t, "2026-07-28", &answerer{answer: accepts}, askOptions{client: func(o *mcp.ClientOptions) {
		o.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	}})
}

var accepted = mcp.InputResponseMap{askKey: &mcp.ElicitResult{Action: "accept"}}

// The first round only asks. The answer counts once, only with the state
// it was asked with, only for that call, and only while fresh; the write
// happens on the verified retry and never again.
func TestTheAnswerIsBoundToItsQuestion(t *testing.T) {
	cs, g := mrtr(t)
	args := askCases["send_message"].args
	first := callRaw(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: args})
	if !first.NeedsInput() || first.RequestState == "" || g.written() != 0 {
		t.Fatalf("first round %+v; %d writes", first, g.written())
	}
	state := first.RequestState
	blocked := func(p *mcp.CallToolParams, want string) {
		t.Helper()
		res := callRaw(t, cs, p)
		if text := textOf(res); !res.IsError || !strings.HasPrefix(text, "[blocked]") || !strings.Contains(text, want) {
			t.Errorf("%s", text)
		}
	}
	blocked(&mcp.CallToolParams{Name: "send_message", Arguments: args, InputResponses: accepted}, "has not asked")
	blocked(&mcp.CallToolParams{Name: "send_message", Arguments: args, InputResponses: accepted, RequestState: state + "x"}, "did not ask")
	other := maps.Clone(args)
	other["text"] = "Something else <users/all>"
	blocked(&mcp.CallToolParams{Name: "send_message", Arguments: other, InputResponses: accepted, RequestState: state}, "another call")
	blocked(&mcp.CallToolParams{Name: "list_spaces", Arguments: map[string]any{}, InputResponses: accepted, RequestState: state},
		"asks the person nothing")
	if g.written() != 0 {
		t.Fatalf("%d writes before the answer", g.written())
	}
	done := callRaw(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: args, InputResponses: accepted, RequestState: state})
	if done.IsError || g.written() != 1 {
		t.Fatalf("the verified retry: %s; %d writes", textOf(done), g.written())
	}
	blocked(&mcp.CallToolParams{Name: "send_message", Arguments: args, InputResponses: accepted, RequestState: state}, "already used")
	if g.written() != 1 {
		t.Fatalf("%d writes after a replay", g.written())
	}
}

// A decline stops the call before anything is read.
func TestADeclineIsRefusedBeforeAnythingRuns(t *testing.T) {
	cs, g := mrtr(t)
	args := askCases["delete_space"].args
	first := callRaw(t, cs, &mcp.CallToolParams{Name: "delete_space", Arguments: args})
	res := callRaw(t, cs, &mcp.CallToolParams{Name: "delete_space", Arguments: args, RequestState: first.RequestState,
		InputResponses: mcp.InputResponseMap{askKey: &mcp.ElicitResult{Action: "decline"}}})
	if text := textOf(res); !res.IsError || !strings.Contains(text, "not confirmed by the person") || g.written() != 0 {
		t.Fatalf("%s; %d writes", text, g.written())
	}
}

// A state that travels through the client expires.
func TestALateAnswerIsRefused(t *testing.T) {
	was := askTTL
	askTTL = -time.Minute
	t.Cleanup(func() { askTTL = was })
	cs, g := mrtr(t)
	args := askCases["delete_message"].args
	first := callRaw(t, cs, &mcp.CallToolParams{Name: "delete_message", Arguments: args})
	res := callRaw(t, cs, &mcp.CallToolParams{Name: "delete_message", Arguments: args, InputResponses: accepted, RequestState: first.RequestState})
	if text := textOf(res); !res.IsError || !strings.Contains(text, "expired") || g.written() != 0 {
		t.Fatalf("%s; %d writes", text, g.written())
	}
}

// A write confirmed and made, whose reply is then lost, is
// [ambiguous_outcome], never "nothing was written".
func TestAReplyLostAfterTheWriteIsAmbiguous(t *testing.T) {
	lose := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if r, ok := res.(*mcp.CallToolResult); ok && err == nil && r.InputRequests == nil && !r.IsError {
				return nil, errors.New("reply lost")
			}
			return res, err
		}
	}
	cs, g := askingSession(t, "2025-11-25", &answerer{answer: accepts}, askOptions{middleware: []mcp.Middleware{lose}})
	res := callRaw(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: askCases["send_message"].args})
	if text := textOf(res); !res.IsError || !strings.HasPrefix(text, "[ambiguous_outcome]") || g.written() != 1 {
		t.Fatalf("%s; %d writes", text, g.written())
	}
}

// A forward into another space lets everyone in it read a message they
// may not have been able to, so it is asked about like a mention of
// everyone. Declined, nothing is posted.
func TestAForwardIntoAnotherSpaceAsks(t *testing.T) {
	p := &answerer{answer: declines}
	cs, g := askingSession(t, "", p, askOptions{})
	res := callRaw(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{
		"space_id": "spaces/AAAAspace2", "text": "fyi",
		"quote_message": "spaces/AAAAspace1/messages/AAAAmsg1", "quote_type": "FORWARD",
	}})
	if text := textOf(res); !res.IsError || !strings.HasPrefix(text, "[blocked]") {
		t.Errorf("a declined forward = %s, want [blocked]", text)
	}
	if g.written() != 0 {
		t.Errorf("declined, and %d writes were sent", g.written())
	}
	qs := p.asked()
	if len(qs) != 1 || !strings.Contains(qs[0].Message, "It forwards a message from `spaces/AAAAspace1`") {
		t.Errorf("questions = %+v, want one naming the space the message comes from", qs)
	}
}

// Forwarding within one space shows its members nothing new, so it is
// not asked about.
func TestAForwardWithinASpaceDoesNotAsk(t *testing.T) {
	p := &answerer{answer: declines}
	cs, g := askingSession(t, "", p, askOptions{})
	res := callRaw(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{
		"space_id": "spaces/AAAAspace1", "text": "fyi",
		"quote_message": "spaces/AAAAspace1/messages/AAAAmsg1", "quote_type": "FORWARD",
	}})
	if res.IsError {
		t.Fatalf("a forward within the space failed: %s", textOf(res))
	}
	if len(p.asked()) != 0 {
		t.Errorf("asked %d questions, want none", len(p.asked()))
	}
	if g.written() != 1 {
		t.Errorf("%d writes, want the one post", g.written())
	}
}
