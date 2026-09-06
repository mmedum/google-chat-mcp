package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// goodTranscript is what the server says when everything works: one
// frame per request, and nothing else on stdout.
const goodTranscript = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"list_spaces"},{"name":"whoami"}]}}
{"jsonrpc":"2.0","id":4,"result":{"resourceTemplates":[{"uriTemplate":"gchat://spaces/{space_id}"}]}}
{"jsonrpc":"2.0","id":3,"result":{"isError":true,"content":[{"type":"text","text":"[auth] no credentials"}]}}
`

func TestCheckTranscript(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		// want is a fragment every reported problem list has to carry;
		// empty means the transcript is clean.
		want string
		// count is the number of problems expected, where it matters.
		count int
	}{
		{name: "a clean run", transcript: goodTranscript},
		{
			name:       "blank lines are not frames and not failures",
			transcript: "\n" + goodTranscript + "\n   \n",
		},
		{
			name:       "a log line on stdout",
			transcript: goodTranscript + "time=2026-09-06 level=INFO msg=serving\n",
			want:       "not a JSON-RPC frame",
			count:      1,
		},
		{
			// The rule is JSON-RPC and nothing else, so valid JSON that
			// is not a frame fails too. A structured log line is exactly
			// this shape, which is how stdout gets polluted in practice.
			name:       "valid JSON that is not a frame",
			transcript: goodTranscript + `{"level":"info","msg":"serving"}` + "\n",
			want:       "not a JSON-RPC frame",
			count:      1,
		},
		{
			name:       "no answer to the tool call",
			transcript: strings.ReplaceAll(goodTranscript, `"id":3`, `"id":9`),
			// Both of the id-3 wants report, and the reason names the
			// missing response rather than the missing text.
			want:  "nothing came back with id 3",
			count: 2,
		},
		{
			// The precision a whole-file grep did not have: the text is
			// in the transcript, on a line that is not the answer to the
			// call that was made.
			name: "the tool error is on somebody else's response",
			transcript: `{"jsonrpc":"2.0","id":1,"result":{}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"list_spaces"},{"name":"whoami"}],"isError":true}}
{"jsonrpc":"2.0","id":4,"result":{"resourceTemplates":[{"uriTemplate":"gchat://spaces/{space_id}"}]}}
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"[auth] no credentials"}]}}
`,
			want:  `does not carry "isError":true`,
			count: 1,
		},
		{
			name:       "a tool error without the auth class",
			transcript: strings.ReplaceAll(goodTranscript, "[auth] no credentials", "something went wrong"),
			want:       "[auth] class",
			count:      1,
		},
		{
			name:       "the server said nothing",
			transcript: "",
			want:       "nothing came back",
			count:      len(smokeWants),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := checkTranscript(tt.transcript, smokeWants)
			if tt.want == "" {
				if len(problems) > 0 {
					t.Fatalf("clean transcript reported %d problem(s): %s", len(problems), strings.Join(problems, "; "))
				}
				return
			}
			if len(problems) != tt.count {
				t.Fatalf("got %d problem(s), want %d: %s", len(problems), tt.count, strings.Join(problems, "; "))
			}
			if !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Errorf("no problem mentions %q:\n%s", tt.want, strings.Join(problems, "\n"))
			}
		})
	}
}

// A want naming an id no request carries can never fail, and reads
// exactly like a want that works. The two lists are written apart, so
// nothing but this holds them together.
func TestEveryWantNamesARequestedID(t *testing.T) {
	asked := map[string]bool{}
	for _, r := range smokeRequests {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(r), &req); err != nil {
			t.Fatalf("request is not valid JSON: %v\n%s", err, r)
		}
		if len(req.ID) > 0 {
			asked[string(req.ID)] = true
		}
	}
	for _, w := range smokeWants {
		if !asked[strconv.Itoa(w.id)] {
			t.Errorf("a want reads the response to id %d and no request asks for it", w.id)
		}
	}
}

func TestAbruptRequestsAreValidJSON(t *testing.T) {
	for _, r := range abruptRequests {
		if !json.Valid([]byte(r)) {
			t.Errorf("request is not valid JSON: %s", r)
		}
	}
	// The whole point of the second run is that it opens a session and
	// then leaves during a call, so it has to carry a call.
	if !strings.Contains(strings.Join(abruptRequests, "\n"), "tools/call") {
		t.Error("the abrupt run makes no tool call, so it leaves during nothing")
	}
}

// The gate's claim is about a server with no credentials. A maintainer
// who has some in the environment would otherwise be running a different
// test from CI's.
func TestSmokeEnvDropsTheServersOwnVariables(t *testing.T) {
	t.Setenv("GCM_REFRESH_TOKEN", "not-a-real-token")
	t.Setenv("GCM_CLIENT_SECRET", "/some/where.json")
	t.Setenv("PATH", os.Getenv("PATH"))

	env := smokeEnv("/tmp/cfg")
	var kept, passed []string
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "GCM_REFRESH_TOKEN"), strings.HasPrefix(kv, "GCM_CLIENT_SECRET"):
			kept = append(kept, kv)
		case strings.HasPrefix(kv, "GCM_"):
			passed = append(passed, kv)
		}
	}
	if len(kept) > 0 {
		t.Errorf("smokeEnv kept credentials from the environment: %s", strings.Join(kept, ", "))
	}
	want := []string{"GCM_CONFIG_DIR=/tmp/cfg", "GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME=1", "GCM_LOG_LEVEL=error"}
	if strings.Join(passed, " ") != strings.Join(want, " ") {
		t.Errorf("smokeEnv passes\n got %v\nwant %v", passed, want)
	}
	if len(env) <= len(want) {
		t.Error("smokeEnv dropped the whole environment; the server still needs PATH and HOME")
	}
}

func TestAbbrev(t *testing.T) {
	if got := abbrev("  short  ", 100); got != "short" {
		t.Errorf("abbrev trims: got %q", got)
	}
	long := strings.Repeat("x", 500)
	got := abbrev(long, 10)
	if !strings.HasPrefix(got, strings.Repeat("x", 10)) || !strings.Contains(got, "500 bytes") {
		t.Errorf("abbrev(long, 10) = %q", got)
	}
	// A cut inside a multi-byte rune would print a replacement
	// character, so the cut lands on a boundary instead.
	runes := strings.Repeat("é", 50)
	if got := abbrev(runes, 11); !strings.Contains(got, "…") || strings.Contains(got, "\uFFFD") {
		t.Errorf("abbrev cut a rune in half: %q", got)
	}
}
