//go:build evals

// Package evals scores whether a model can do the job through these
// tools. Each task creates one scratch space through the server, runs
// Claude Code headless with only this server available, then reads the
// space back through the server and inspects the tool-call trace.
//
//	make build && go test -tags=evals ./internal/evals -v -timeout 40m
//	go test -tags=evals ./internal/evals -v -run TestEvals/post-verbatim
//
// Never part of CI. It needs a login, a working `claude` CLI, and it
// spends real API usage — budget roughly a couple of dollars for the set,
// and EVAL_MODEL=sonnet is cheaper. It also writes to a real Google
// Workspace: every task makes its own space, posts to it, and deletes it
// again unless the task failed or EVAL_KEEP=1 is set. It never touches a
// space it did not create.
//
// Traces and report.md land in $LIVE_OUT/evals, outside the repository.
// A result is saved only when the call that produced it named the
// scratch space; see redact. That is the standard's rule for a live run
// — what gets written down is only what the run itself created.
package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpName is what the client calls this server, and so the prefix on
// every tool name in a trace.
const mcpName = "gchat"

// spacePrefix names a scratch space so it is obvious in a sidebar what
// it is and that it can go.
const spacePrefix = "google-chat-mcp evals"

// server is a client of our own binary, used to seed a space and to
// score it afterwards — never by the model under test.
type server struct {
	t   *testing.T
	cs  *mcp.ClientSession
	ctx context.Context
	dir string // GCM_LOCAL_DIR, shared with the server the model drives
}

func binPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../google-chat-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("build the binary first (make build): %v", err)
	}
	return p
}

// localDir is the one directory file transfer is allowed to touch. It
// lives under the run's output directory so an eval never reads or
// writes anywhere else on the machine.
func localDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(outDir(t), "files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func connect(t *testing.T) *server {
	t.Helper()
	dir := localDir(t)
	cmd := exec.Command(binPath(t))
	// Deletes are allowed here on purpose. They are refused by default now
	// — a deleted Chat message has no trash behind it — and this suite
	// exercises them deliberately against a scratch space it made itself,
	// then deletes that space to clean up. Without this the run would fail
	// its delete steps AND leave the scratch space behind in a real account,
	// which is worse than the failure.
	cmd.Env = append(os.Environ(), "GCM_LOG_LEVEL=warn", "GCM_LOCAL_DIR="+dir,
		"GCM_ALLOW_DESTRUCTIVE=true")
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "evals", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &server{t: t, cs: cs, ctx: ctx, dir: dir}
}

// must runs a tool for seeding or scoring. A failure here is the
// harness's problem, not the model's, so it stops the task rather than
// counting against the model.
func (s *server) must(name string, args map[string]any) map[string]any {
	s.t.Helper()
	res, err := s.cs.CallTool(s.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		s.t.Fatalf("harness step %s failed: %s", name, b.String())
	}
	sc, _ := res.StructuredContent.(map[string]any)
	return sc
}

// into re-decodes a structured result into a typed value, so a check
// reads a field rather than asserting on a map.
func (s *server) into(sc map[string]any, v any) {
	s.t.Helper()
	raw, err := json.Marshal(sc)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		s.t.Fatalf("decode %T: %v", v, err)
	}
}

// seed makes the scratch space this task will work in.
func (s *server) seed(name string) string {
	s.t.Helper()
	var out struct {
		SpaceID string `json:"space_id"`
	}
	s.into(s.must("create_space", map[string]any{
		"display_name": fmt.Sprintf("%s %s %d (safe to delete)", spacePrefix, name, time.Now().Unix()),
	}), &out)
	if out.SpaceID == "" {
		s.t.Fatal("create_space returned no space_id")
	}
	return out.SpaceID
}

// drop deletes the scratch space, which cascades to its messages. A
// failed task keeps its space so the evidence survives.
func (s *server) drop(space string, failed bool) {
	s.t.Helper()
	if os.Getenv("EVAL_KEEP") != "" || failed {
		s.t.Logf("space kept for inspection: %s", space)
		return
	}
	s.must("delete_space", map[string]any{"space_id": space, "confirm_space_id": space})
}

type message struct {
	MessageID   string `json:"message_id"`
	Text        string `json:"text"`
	ThreadID    string `json:"thread_id"`
	SenderEmail string `json:"sender_email"`
	Attachments []struct {
		AttachmentName string `json:"attachment_name"`
		FileName       string `json:"file_name"`
	} `json:"attachments"`
}

// post writes a message as the harness, which is how a task gets
// something for the model to act on. thread may be empty.
func (s *server) post(space, text, thread string) message {
	s.t.Helper()
	args := map[string]any{"space_id": space, "text": text}
	if thread != "" {
		args["thread_name"] = thread
	}
	var out struct {
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id"`
	}
	s.into(s.must("send_message", args), &out)
	if out.MessageID == "" {
		s.t.Fatal("send_message returned no message_id")
	}
	return message{MessageID: out.MessageID, Text: text, ThreadID: out.ThreadID}
}

func (s *server) messages(space string) []message {
	s.t.Helper()
	var out struct {
		Result []message `json:"result"`
	}
	s.into(s.must("get_messages", map[string]any{"space_id": space, "limit": 50}), &out)
	return out.Result
}

// message reads one back. found is false when Google says it is gone.
func (s *server) message(name string) (message, bool) {
	s.t.Helper()
	res, err := s.cs.CallTool(s.ctx, &mcp.CallToolParams{
		Name: "get_message", Arguments: map[string]any{"message_name": name}})
	if err != nil {
		s.t.Fatalf("get_message: %v", err)
	}
	if res.IsError {
		return message{}, false
	}
	var m message
	sc, _ := res.StructuredContent.(map[string]any)
	s.into(sc, &m)
	return m, true
}

func (s *server) reactions(name string) []struct {
	Emoji        string `json:"emoji"`
	ReactionName string `json:"reaction_name"`
} {
	s.t.Helper()
	var out struct {
		Reactions []struct {
			Emoji        string `json:"emoji"`
			ReactionName string `json:"reaction_name"`
		} `json:"reactions"`
	}
	s.into(s.must("list_reactions", map[string]any{"message_name": name}), &out)
	return out.Reactions
}

func (s *server) pins(space string) []string {
	s.t.Helper()
	var out struct {
		Result []struct {
			MessageID string `json:"message_id"`
		} `json:"result"`
	}
	s.into(s.must("list_pinned_messages", map[string]any{"space_id": space}), &out)
	ids := make([]string, 0, len(out.Result))
	for _, p := range out.Result {
		ids = append(ids, p.MessageID)
	}
	return ids
}

func (s *server) sectionItems(section string) []string {
	s.t.Helper()
	var out struct {
		Items []struct {
			SpaceID *string `json:"space_id"`
		} `json:"items"`
	}
	s.into(s.must("list_section_items", map[string]any{"section_name": section}), &out)
	var spaces []string
	for _, it := range out.Items {
		if it.SpaceID != nil {
			spaces = append(spaces, *it.SpaceID)
		}
	}
	return spaces
}

func (s *server) readState(space string) string {
	s.t.Helper()
	var out struct {
		LastReadTime string `json:"last_read_time"`
	}
	s.into(s.must("get_space_read_state", map[string]any{"space_id": space}), &out)
	return out.LastReadTime
}

// writeFile puts a file in the one directory transfer is allowed to
// touch, and returns its path and SHA-256.
func (s *server) writeFile(name string, body []byte) (path, sum string) {
	s.t.Helper()
	path = filepath.Join(s.dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		s.t.Fatal(err)
	}
	return path, sha256Of(s.t, path)
}
