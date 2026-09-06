package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts a schema dump in a temp file and returns its path.
func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const baseline = `{"version":"0.9.0","tools":[
  {"name":"get_space","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
   "outputSchema":{"properties":{"space_id":{},"display_name":{}}}},
  {"name":"send_message","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
   "outputSchema":{"properties":{"message_id":{}}}}
]}`

func TestSchemaDiffClean(t *testing.T) {
	// Same tools and output fields, one input reshaped: a schema that
	// gained an argument breaks no caller, so it must not fail.
	current := `{"version":"2.0.0","tools":[
      {"name":"get_space","inputSchema":{"type":"object","properties":{"space_id":{"type":"string"}}},
       "outputSchema":{"properties":{"space_id":{},"display_name":{}}}},
      {"name":"send_message","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
       "outputSchema":{"properties":{"message_id":{}}}}
    ]}`
	var out, errOut bytes.Buffer
	code := schemaDiff(write(t, "old.json", baseline), write(t, "new.json", current), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "baseline 0.9.0: 2 tools; built: 2 tools") {
		t.Errorf("missing header: %s", out.String())
	}
	if !strings.Contains(out.String(), "inputs reshaped, look at these (1): get_space") {
		t.Errorf("reshaped input not reported: %s", out.String())
	}
}

func TestSchemaDiffFailures(t *testing.T) {
	tests := []struct {
		name    string
		current string
		want    string
	}{
		{
			name: "missing tool",
			current: `{"version":"2.0.0","tools":[
              {"name":"get_space","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
               "outputSchema":{"properties":{"space_id":{},"display_name":{}}}}]}`,
			want: "missing from the build:\n  send_message",
		},
		{
			// A rename is caught by the half that goes missing. The new
			// name is reported as an addition, which is what a genuinely
			// new tool is, and the run still fails.
			name: "renamed tool",
			current: `{"version":"2.0.0","tools":[
              {"name":"get_space","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
               "outputSchema":{"properties":{"space_id":{},"display_name":{}}}},
              {"name":"post_message","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
               "outputSchema":{"properties":{"message_id":{}}}}]}`,
			want: "missing from the build:\n  send_message",
		},
		{
			name: "lost an output field",
			current: `{"version":"2.0.0","tools":[
              {"name":"get_space","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
               "outputSchema":{"properties":{"space_id":{}}}},
              {"name":"send_message","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
               "outputSchema":{"properties":{"message_id":{}}}}]}`,
			want: "get_space: display_name",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := schemaDiff(write(t, "old.json", baseline), write(t, "new.json", tc.current), &out, &errOut)
			if code != 1 {
				t.Fatalf("exit %d, want 1: %s", code, out.String())
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("want %q in output, got: %s", tc.want, out.String())
			}
		})
	}
}

// A reordered key is the same schema. Comparing raw bytes would report
// every tool as reshaped and the signal would be worthless.
func TestSchemaDiffIgnoresKeyOrder(t *testing.T) {
	current := `{"version":"2.0.0","tools":[
      {"name":"get_space","inputSchema":{"properties":{"payload":{"type":"object"}},"type":"object"},
       "outputSchema":{"properties":{"display_name":{},"space_id":{}}}},
      {"name":"send_message","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
       "outputSchema":{"properties":{"message_id":{}}}}
    ]}`
	var out, errOut bytes.Buffer
	if code := schemaDiff(write(t, "old.json", baseline), write(t, "new.json", current), &out, &errOut); code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out.String())
	}
	if strings.Contains(out.String(), "reshaped") {
		t.Errorf("key order reported as a reshape: %s", out.String())
	}
}

func TestToolNamesSorted(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"tool-names", write(t, "d.json", baseline)}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if out.String() != "get_space\nsend_message\n" {
		t.Errorf("got %q", out.String())
	}
}

// The staleness gate reads this list, so a variable the server reads and
// this misses is a documentation hole the gate cannot see.
func TestConfigVars(t *testing.T) {
	got := configVars()
	want := []string{
		"GCM_ALLOW_DESTRUCTIVE",
		"GCM_CHAT_API_BASE",
		"GCM_CLIENT_SECRET",
		"GCM_CONFIG_DIR",
		"GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME",
		"GCM_DIRECTORY_CACHE_TTL_SECONDS",
		"GCM_HTTP_MAX_RETRIES",
		"GCM_HTTP_TIMEOUT_SECONDS",
		"GCM_INTERACTION_HINT",
		"GCM_LOCAL_DIR",
		"GCM_LOG_FORMAT",
		"GCM_LOG_LEVEL",
		"GCM_PEOPLE_API_BASE",
		"GCM_PROFILE",
		"GCM_READ_ONLY",
		"GCM_REFRESH_TOKEN",
		"GCM_SEARCH_MAX_PAGES",
		"GCM_TOOLSETS",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("config vars:\n got %v\nwant %v", got, want)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"nonsense"},
		{"tool-names"},                       // a required argument missing
		{"release-notes"},                    // the same, for a command with an optional second
		{"config-vars", "extra"},             // a command that takes none
		{"schema-diff", "one", "two"},        // past the optional argument
		{"release-notes", "1.0.0", "a", "b"}, // the same
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
		// The usage text is generated from the command list, so this
		// asserts the heading and that at least one command reached it —
		// a usage block listing nothing would otherwise pass.
		if !strings.Contains(errOut.String(), "Usage:") {
			t.Errorf("run(%v) printed no usage:\n%s", args, errOut.String())
		}
		for name := range commands {
			if !strings.Contains(errOut.String(), name) {
				t.Errorf("run(%v) printed a usage block missing %q", args, name)
			}
		}
	}
}

func TestUnreadableFiles(t *testing.T) {
	good := write(t, "good.json", baseline)
	bad := write(t, "bad.json", "{not json")

	// The comparison is called directly. Routing these through run()
	// would spend three arguments on a subcommand that takes one, so
	// they would return 2 for a usage error and pass while testing
	// nothing about unreadable files.
	for _, tc := range []struct{ name, a, b string }{
		{"baseline absent", filepath.Join(t.TempDir(), "absent.json"), good},
		{"current unparseable", good, bad},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := schemaDiff(tc.a, tc.b, &out, &errOut); code != 2 {
				t.Errorf("schemaDiff(%s, %s) = %d, want 2", tc.a, tc.b, code)
			}
		})
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"tool-names", bad}, &out, &errOut); code != 2 {
		t.Errorf("tool-names on unparseable input = %d, want 2", code)
	}
}
