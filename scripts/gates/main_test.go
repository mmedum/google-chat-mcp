package main

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	code := schemaDiff(write(t, "old.json", baseline), write(t, "new.json", current), "", &out, &errOut)
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
			code := schemaDiff(write(t, "old.json", baseline), write(t, "new.json", tc.current), "", &out, &errOut)
			if code != 1 {
				t.Fatalf("exit %d, want 1: %s", code, out.String())
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("want %q in output, got: %s", tc.want, out.String())
			}
		})
	}
}

// A baseline from an older release protects an older surface: a tool
// added since could be dropped and nothing would fail. The build here is
// the baseline itself, so only the version it names can fail it.
func TestSchemaDiffBaselineMustBeTheWantedVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		code int
		out  string
	}{
		{name: "baseline is the wanted version", want: "0.9.0", code: 0},
		{name: "nothing wanted yet", want: "", code: 0},
		{
			name: "baseline is older", want: "1.0.0", code: 1,
			out: "FAIL: the baseline is the 0.9.0 surface, but it must be 1.0.0's",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, "baseline.json", baseline)
			var out, errOut bytes.Buffer
			code := schemaDiff(path, path, tc.want, &out, &errOut)
			if code != tc.code {
				t.Fatalf("want %q: exit %d, want %d: %s", tc.want, code, tc.code, out.String())
			}
			if !strings.Contains(out.String(), tc.out) {
				t.Errorf("want %q: want %q in output, got: %s", tc.want, tc.out, out.String())
			}
		})
	}
}

// The baseline is refreshed in the release commit, so there the version
// being cut is the one it must hold. Unreleased work above the newest
// heading changes nothing: it has no surface to protect yet.
func TestBaselineVersion(t *testing.T) {
	for _, tc := range []struct {
		name, changelog, want string
	}{
		{
			name:      "between releases",
			changelog: "# Changelog\n\n## [Unreleased]\n\n- **Added:** a thing.\n\n## [4.0.0] - 2026-10-03\n",
			want:      "v4.0.0",
		},
		{
			name:      "release commit",
			changelog: "# Changelog\n\n## [Unreleased]\n\n## [4.1.0] - 2026-10-09\n\n- **Added:** a thing.\n\n## [4.0.0] - 2026-10-03\n",
			want:      "v4.1.0",
		},
		{name: "no release yet", changelog: "# Changelog\n\n## [Unreleased]\n", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := baselineVersion(tc.changelog); got != tc.want {
				t.Errorf("baselineVersion(%s changelog) = %q, want %q", tc.name, got, tc.want)
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
	if code := schemaDiff(write(t, "old.json", baseline), write(t, "new.json", current), "", &out, &errOut); code != 0 {
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
		"GCM_ASK_BEFORE_SEND",
		"GCM_CHAT_API_BASE",
		"GCM_CLIENT_SECRET",
		"GCM_CLOUD_IDENTITY_API_BASE",
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
		"GCM_REQUIRE_PROMPT",
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
			if code := schemaDiff(tc.a, tc.b, "", &out, &errOut); code != 2 {
				t.Errorf("schemaDiff(%s, %s) = %d, want 2", tc.a, tc.b, code)
			}
		})
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"tool-names", bad}, &out, &errOut); code != 2 {
		t.Errorf("tool-names on unparseable input = %d, want 2", code)
	}
}

// A command added without a runsIn declares no pipeline, and the parity
// gate has nothing to say about it — so a new gate wired into the
// Makefile and CI but not into the registry would leave everything
// green. That is exactly the drift the field was introduced to catch,
// which makes "forgot to set it" the failure worth refusing.
func TestEveryCommandDeclaresWhereItRuns(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		switch commands[name].runsIn {
		case manual, inCheck, inRelease:
		default:
			t.Errorf("%q declares no runsIn. Say which pipeline has to run it: inCheck for "+
				"`make check` and CI, inRelease for the release, manual for a query typed "+
				"by hand.", name)
		}
	}
	// Both pipelines have to hold something, or the parity gate that
	// reads them is asserting nothing.
	for _, w := range []struct {
		name string
		in   where
	}{{"inCheck", inCheck}, {"inRelease", inRelease}} {
		if len(commandsRunningIn(w.in)) == 0 {
			t.Errorf("no command runs in %s: the parity gate for it is looking at nothing", w.name)
		}
	}
}

// fakeServerDump names a schema dump. A test that needs a server binary
// runs this test binary with it set, and TestMain prints that dump the
// way --dump-schemas would. It refuses when GCM_READ_ONLY reached it,
// because the gates dump the default surface whatever the shell sets.
const fakeServerDump = "GATES_TEST_FAKE_SERVER_DUMP"

func TestMain(m *testing.M) {
	if path := os.Getenv(fakeServerDump); path != "" {
		os.Exit(fakeServer(path))
	}
	os.Exit(m.Run())
}

func fakeServer(path string) int {
	if _, set := os.LookupEnv("GCM_READ_ONLY"); set {
		_, _ = os.Stderr.WriteString("fake server: GCM_READ_ONLY reached it\n")
		return 3
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 4
	}
	_, _ = os.Stdout.Write(b)
	return 0
}

// surface is the baseline fixture stamped with another version.
func surface(version string) string {
	return strings.Replace(baseline, `"version":"0.9.0"`, `"version":"`+version+`"`, 1)
}

// withFindSpace is the fixture with one more tool, stamped version.
func withFindSpace(version string) string {
	return strings.Replace(surface(version), `"tools":[`,
		`"tools":[{"name":"find_space","inputSchema":{"type":"object"},"outputSchema":{"properties":{"space_id":{}}}},`, 1)
}

// releaseTree runs the test from a temporary checkout whose changelog's
// newest release is 1.0.0, with unreleased under [Unreleased], and with
// baselineBody as the schema baseline unless it is empty. It returns a
// server binary that dumps dumpBody.
func releaseTree(t *testing.T, unreleased, baselineBody, dumpBody string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	files := map[string]string{
		changelogPath: "# Changelog\n\n## [Unreleased]\n\n" + unreleased +
			"\n\n## [1.0.0] - 2026-10-09\n\n## [0.9.0] - 2026-10-01\n",
		"fake-dump.json": dumpBody,
	}
	if baselineBody != "" {
		files[baselinePath] = baselineBody
	}
	if err := os.Mkdir(filepath.Dir(baselinePath), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(fakeServerDump, filepath.Join(dir, "fake-dump.json"))
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// The command, not only the comparison: what the baseline must hold
// comes from the changelog on disk.
func TestSchemaDiffBinary(t *testing.T) {
	const added = "- **Added:** `find_space`."
	for _, tc := range []struct {
		name, unreleased, baseline, dump string
		code                             int
		out                              string
	}{
		{name: "baseline is the newest release", baseline: surface("v1.0.0"), dump: surface("dev"), code: 0},
		{
			name: "baseline is an older release", baseline: surface("v0.9.0"), dump: surface("dev"), code: 1,
			out: "FAIL: the baseline is the v0.9.0 surface, but it must be v1.0.0's",
		},
		{
			name: "baseline is missing after a release", baseline: "", dump: surface("dev"), code: 1,
			out: "FAIL: CHANGELOG.md names v1.0.0 as released, but testdata/schemas-baseline.json is missing",
		},
		{
			name: "nothing unreleased, and the build adds a tool", baseline: surface("v1.0.0"), dump: withFindSpace("dev"), code: 1,
			out: "FAIL: nothing is under [Unreleased], so this build is v1.0.0, and its tools differ from the baseline",
		},
		{
			name: "unreleased work adds a tool", unreleased: added,
			baseline: surface("v1.0.0"), dump: withFindSpace("dev"), code: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := releaseTree(t, tc.unreleased, tc.baseline, tc.dump)
			var out, errOut bytes.Buffer
			if code := schemaDiffBinary(bin, &out, &errOut); code != tc.code {
				t.Fatalf("exit %d, want %d: %s%s", code, tc.code, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), tc.out) {
				t.Errorf("want %q in output, got: %s", tc.out, out.String())
			}
		})
	}
}

// A setting such as read-only mode changes what registers, so a gate run
// from a shell that exports one would compare or record the wrong surface.
func TestTheDumpIgnoresServerSettings(t *testing.T) {
	bin := releaseTree(t, "", surface("v1.0.0"), surface("dev"))
	t.Setenv("GCM_READ_ONLY", "true")
	var out, errOut bytes.Buffer
	if code := schemaDiffBinary(bin, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, want 0: %s%s", code, out.String(), errOut.String())
	}
}

func TestSchemaBaseline(t *testing.T) {
	dropped := `{"version":"v1.0.0","tools":[
      {"name":"get_space","inputSchema":{"type":"object","properties":{"payload":{"type":"object"}}},
       "outputSchema":{"properties":{"space_id":{},"display_name":{}}}}]}`
	for _, tc := range []struct {
		name, baseline, dump string
		code                 int
		out                  string
		written              bool
	}{
		{
			name: "records a build that adds a tool", baseline: surface("v0.9.0"), dump: withFindSpace("v1.0.0"),
			code: 0, out: "is now the v1.0.0 surface: 3 tools", written: true,
		},
		{
			name: "records the first release", baseline: "", dump: withFindSpace("v1.0.0"),
			code: 0, out: "is now the v1.0.0 surface: 3 tools", written: true,
		},
		{
			name: "refuses a build that drops a tool", baseline: surface("v0.9.0"), dump: dropped,
			code: 1, out: "missing from the build:\n  send_message",
		},
		{
			// A second run in the same release compares with the first
			// run's baseline, so it says how to compare with the release.
			name:     "refuses a second run that drops a tool new in this release",
			baseline: withFindSpace("v1.0.0"), dump: surface("v1.0.0"),
			code: 1, out: "It already holds v1.0.0 from an earlier run",
		},
		{
			name: "refuses a build of another version", baseline: surface("v0.9.0"), dump: surface("dev"),
			code: 1, out: "is stamped dev, but the release being cut is v1.0.0. Build it with `make build VERSION=v1.0.0`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := releaseTree(t, "", tc.baseline, tc.dump)
			var out, errOut bytes.Buffer
			if code := schemaBaseline(bin, &out, &errOut); code != tc.code {
				t.Fatalf("exit %d, want %d: %s%s", code, tc.code, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), tc.out) {
				t.Errorf("want %q in output, got: %s", tc.out, out.String())
			}
			got, err := os.ReadFile(baselinePath)
			if err != nil && tc.written {
				t.Fatal(err)
			}
			want := tc.baseline
			if tc.written {
				want = tc.dump
			}
			if string(got) != want {
				t.Errorf("baseline after the run:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
