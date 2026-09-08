package tools

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
)

// The rule this file enforces: a log line may say when a call happened,
// where it went, and how it ended. It may not identify or reconstruct
// the subject. A chat server's payloads are other people's messages, so
// the whole of OWASP's "no tokens, no personal data" applies to the
// content, not only to credentials.
//
// The deliberate exception is resource ids. A space or message name is
// how an operator follows something up, the logs go to their own
// stderr, and an id on its own says nothing about what was said. They
// are logged on purpose and asserted for below, so this test also fails
// if the logging is switched off rather than made safe.
//
// Held by construction today. Nothing stops the next handler from
// passing a message body to slog, which is what this test is for.
const (
	canaryText   = "CANARY-message-body-never-log-this"
	canaryEmail  = "canary.person@example.com"
	canaryName   = "Canary Person"
	canarySpace  = "CANARY-space-display-name"
	canaryToken  = "CANARY-access-token"
	canaryQuery  = "CANARY-search-term"
	canaryDrift  = "CANARY-unknown-field-value"
	canaryReason = "CANARY-refusal-detail"
)

// forbidden is every canary, with what putting it in a log would give
// away.
var forbidden = map[string]string{
	canaryText:   "message text",
	canaryEmail:  "an email address",
	canaryName:   "a person's display name",
	canarySpace:  "a space's display name",
	canaryToken:  "the access token",
	canaryQuery:  "what the caller searched for",
	canaryDrift:  "the value behind an unknown field",
	canaryReason: "Google's refusal detail, which quotes the request",
}

// canaryGoogle answers every endpoint these tools reach with a payload
// stuffed with canaries, so anything a handler decides to log is
// something this test can catch.
func canaryGoogle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		path := r.URL.Path
		switch {
		// A People search puts the caller's search term in the query
		// string. Dropping the connection is how a transport error
		// gets raised with that URL inside it, which is the shape that
		// leaked before withoutURL.
		case strings.Contains(path, "searchContacts"):
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server does not support hijacking")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
		case strings.Contains(path, "searchDirectoryPeople"):
			if got := r.URL.Query().Get("query"); got != canaryQuery {
				t.Errorf("directory query = %q, want the canary", got)
			}
			fmt.Fprint(w, peopleSearch("people/1", canaryEmail, canaryName))
		case strings.HasPrefix(path, "/people"):
			peopleBatch(canaryEmail, canaryName)(w, r)
		case strings.HasPrefix(path, "/oidc"):
			fmt.Fprintf(w, `{"sub":"1","email":%q,"name":%q}`, canaryEmail, canaryName)
		case strings.Contains(path, "findDirectMessage"):
			// The lookup carries the target's address in the query
			// string, the same hazard as the People search.
			http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
		case strings.Contains(path, "spaces:setup"):
			fmt.Fprintf(w, `{"name":"spaces/AAAAdm1","spaceType":"DIRECT_MESSAGE"}`)
		case strings.HasSuffix(path, "/messages") && r.Method == http.MethodGet:
			// An unknown field with a canary value: the drift reporter
			// must log the field name and never what was in it.
			fmt.Fprintf(w, `{"messages":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1",
			  "sender":{"name":"users/1","displayName":%q},"createTime":"2026-01-02T03:04:05Z",
			  "text":%q,"thread":{"name":"spaces/AAAAspace1/threads/AAAAthread1"},
			  "canaryUnknownField":%q}]}`, canaryName, canaryText, canaryDrift)
		case strings.HasSuffix(path, "/messages") && r.Method == http.MethodPost:
			fmt.Fprintf(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1","text":%q,
			  "createTime":"2026-01-02T03:04:05Z"}`, canaryText)
		case path == "/v1/spaces":
			fmt.Fprintf(w, `{"spaces":[{"name":"spaces/AAAAspace1","spaceType":"SPACE","displayName":%q}]}`, canarySpace)
		default:
			// A refusal whose detail quotes the request back. Google
			// really does this, and it is the least obvious way a
			// payload reaches a log.
			http.Error(w, fmt.Sprintf(`{"error":{"code":403,"message":%q}}`, canaryReason), http.StatusForbidden)
		}
	}
}

// TestLogsNeverCarryThePayload drives the tools that touch a payload and
// reads back everything the server logged at debug level.
func TestLogsNeverCarryThePayload(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cs := sessionWithLogger(t, canaryGoogle(t), config.Config{Toolsets: config.AllToolsets}, log)

	// Every registered tool, not a hand-picked few: a tool that logs a
	// message body is caught whether or not anyone remembered to add it
	// here. A refusal is as interesting as a success, so no result is
	// checked — the error paths are where a payload reaches a log.
	for name, args := range everyRegisteredTool(t, cs) {
		if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
			Name: name, Arguments: withCanaries(args),
		}); err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
	}

	out := logs.String()
	if strings.TrimSpace(out) == "" {
		t.Fatal("nothing was logged; this test would pass on a server that logs nothing")
	}
	for canary, what := range forbidden {
		if strings.Contains(out, canary) {
			t.Errorf("the logs carry %s:\n%s", what, lineWith(out, canary))
		}
	}

	// The deliberate exception, asserted so it stays deliberate: a
	// resource id is logged, because it is what an operator follows up
	// with and it discloses nothing about the content.
	if !strings.Contains(out, "spaces/AAAAdm1") {
		t.Errorf("no resource id in the logs; the correlation the exception exists for is gone:\n%s", out)
	}
	// The drift reporter names the field, which is Google's own schema
	// and safe, while the value behind it is checked above.
	if !strings.Contains(out, "canaryUnknownField") {
		t.Errorf("schema drift was not reported:\n%s", out)
	}
}

// lineWith returns the log lines containing s, so a failure names the
// call site instead of printing the whole buffer.
func lineWith(out, s string) string {
	var hits []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, s) {
			hits = append(hits, line)
		}
	}
	return strings.Join(hits, "\n")
}

// withCanaries swaps the payload-carrying arguments for markers, so the
// caller's own words are traceable through the logs as well as
// Google's. The search term matters most: it reaches a log through the
// request URL rather than through anything a handler chose to log.
func withCanaries(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		switch k {
		case "text", "regex":
			out[k] = canaryText
		case "query":
			out[k] = canaryQuery
		case "user_email":
			out[k] = canaryEmail
		case "display_name":
			out[k] = canarySpace
		default:
			out[k] = v
		}
	}
	return out
}
