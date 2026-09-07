//go:build live

// Package livecheck drives the shipped binary against a real account and
// checks what Google actually answers.
//
// It is not the stdio smoke test, which speaks the protocol with no
// credentials, and it is not the evals, which score a model. This runs
// the server's own tools against the live API and asserts the server's
// behaviour — the only thing in this repository that can catch the class
// of bug the unit suite structurally cannot, because every fake here is
// written from what we believe the API does. Seven rows of the evidence
// log in docs/architecture.md were settled by a live call contradicting
// Google's own reference; none of them could have been found by a test.
//
// Two rules make it safe to run against a real Workspace. Everything
// happens inside one scratch space the run creates and deletes, so
// nothing it touches belongs to anybody. And a step may print only an
// identifier this run made — see redact — because no pattern can tell an
// invented space id from a real one.
//
// Run it with `make live`. It needs a login, and it writes to a real
// account, so it is never in CI.
package livecheck

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

// spacePrefix names the scratch space so it is obvious in a sidebar what
// it is and that it can go, if a failed run leaves one behind.
const spacePrefix = "livecheck"

// driver is a client of our own binary.
type driver struct {
	t   *testing.T
	cs  *mcp.ClientSession
	ctx context.Context
	dir string // GCM_LOCAL_DIR: the one directory file transfer may touch

	// space is the scratch space every step works in, and the only
	// identifier a report may print unredacted.
	space string
	// made holds every resource name this run created, so redact can
	// tell them from everything else.
	made map[string]bool

	// What earlier steps wrote, for the steps that read it back.
	posted string
	thread string
	member string
	// email is this account's own address, which remove_reaction needs:
	// a reaction belongs to a person, so removing one says whose.
	email string
	// event is one event name from the space's own feed, for get_space_event.
	event string
	// attached is the message carrying the uploaded file.
	attached string
	// uploadToken is spent by the post that carries it.
	uploadToken string
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

// connect starts the binary and speaks MCP to it over stdio, which is
// the transport a client uses. Driving the package directly would skip
// the registration, the schemas and the rendering — the parts most
// likely to be wrong in a way only a client would see.
func connect(t *testing.T) *driver {
	t.Helper()
	dir := t.TempDir()
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
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "livecheck", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &driver{t: t, cs: cs, ctx: ctx, dir: dir, made: map[string]bool{}}
}

// result is one tool call's answer, in both halves.
type result struct {
	Structured map[string]any
	Text       string
	IsError    bool
}

// call runs a tool and returns what came back, including a refusal. A
// step that expects a refusal needs the refusal, not a failed test.
func (d *driver) call(name string, args map[string]any) result {
	d.t.Helper()
	res, err := d.cs.CallTool(d.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		d.t.Fatalf("%s: transport: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	sc, _ := res.StructuredContent.(map[string]any)
	return result{Structured: sc, Text: text.String(), IsError: res.IsError}
}

// must runs a tool that has to succeed. A failure here stops the run:
// later steps read what this one wrote.
func (d *driver) must(name string, args map[string]any) map[string]any {
	d.t.Helper()
	res := d.call(name, args)
	if res.IsError {
		d.t.Fatalf("%s failed: %s", name, d.redact(res.Text))
	}
	return res.Structured
}

// into re-decodes a structured result so a check reads a field rather
// than asserting on a map.
func (d *driver) into(sc map[string]any, v any) {
	d.t.Helper()
	raw, err := json.Marshal(sc)
	if err != nil {
		d.t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		d.t.Fatalf("decode %T: %v", v, err)
	}
}

// seed makes the scratch space. Everything else this run does happens
// inside it.
func (d *driver) seed() {
	d.t.Helper()
	var out struct {
		SpaceID string `json:"space_id"`
	}
	d.into(d.must("create_space", map[string]any{
		"display_name": fmt.Sprintf("%s %d (safe to delete)", spacePrefix, time.Now().Unix()),
	}), &out)
	if out.SpaceID == "" {
		d.t.Fatal("create_space returned no space_id")
	}
	d.space = out.SpaceID
	d.record(out.SpaceID)
}

// record remembers a resource this run created, so redact will let it
// through.
func (d *driver) record(name string) {
	if name != "" {
		d.made[name] = true
	}
}

// drop deletes the scratch space, which takes its messages with it. A
// failed run keeps it, because the evidence is in it.
func (d *driver) drop() {
	d.t.Helper()
	if d.space == "" {
		return
	}
	if d.t.Failed() || os.Getenv("LIVE_KEEP") != "" {
		d.t.Logf("space kept for inspection: %s", d.space)
		return
	}
	res := d.call("delete_space", map[string]any{
		"space_id": d.space, "confirm_space_id": d.space,
	})
	if res.IsError {
		d.t.Errorf("the scratch space was left behind: %s", d.redact(res.Text))
	}
}

// redact replaces every identifier the run did not itself create.
//
// Structural, not a matter of care: a message body here is ordinary
// English and a real space id is indistinguishable from an invented one,
// so nothing downstream can filter what this does not filter. The
// account's own address is replaced too — it is in every whoami answer.
func (d *driver) redact(s string) string {
	for _, pat := range []string{
		`spaces/`, `users/`,
	} {
		s = redactPrefixed(s, pat, d.made)
	}
	return emailPattern.ReplaceAllString(s, "<address>")
}

// redactPrefixed walks the string for prefix+id runs and replaces any
// the run did not create.
func redactPrefixed(s, prefix string, keep map[string]bool) string {
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i:]
		end := len(rest)
		for j, r := range rest {
			if j < len(prefix) {
				continue
			}
			if !isNameRune(r) {
				end = j
				break
			}
		}
		name := rest[:end]
		if keep[name] || keepsPrefixOf(keep, name) {
			b.WriteString(name)
		} else {
			b.WriteString(prefix + "<redacted>")
		}
		s = rest[end:]
	}
}

// keepsPrefixOf reports whether name sits under something this run made,
// which is how a message inside the scratch space stays readable.
func keepsPrefixOf(keep map[string]bool, name string) bool {
	for made := range keep {
		if strings.HasPrefix(name, made+"/") {
			return true
		}
	}
	return false
}

// writeLocal puts a file in the one directory file transfer may touch,
// and hands back its path. The bytes are ordinary words: an upload
// leaves a file on a real account until the space goes.
func (d *driver) writeLocal(name, body string) string {
	d.t.Helper()
	path := filepath.Join(d.dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		d.t.Fatal(err)
	}
	return path
}

func isNameRune(r rune) bool {
	return r == '/' || r == '-' || r == '_' || r == '.' ||
		(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
