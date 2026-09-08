package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/mmedum/google-chat-mcp/v2/internal/server"
)

// The Claude Desktop bundle is built once a year at most, on a runner,
// from a manifest nobody reads in between. These tests are what stands
// between a typo in it and a release nobody can install: the manifest is
// checked against the published schema and against the code, and the
// pack script is run for real against a fake dist tree, so the paths the
// manifest names have to be the paths the bundle carries.
//
// testdata/mcpb-manifest-v0.3.schema.json is
// anthropics/mcpb schemas/mcpb-manifest-v0.3.schema.json, fetched
// 2026-09-06, byte-identical to that repository's "latest" schema.

// alwaysSubstituted names the references the installing client may leave
// standing. A setting that is neither required nor defaulted is one the
// person installing can skip, and what the server is handed then is not
// documented anywhere: an empty value if the client is kind, the literal
// ${user_config.key} if it is not. The second starts a server with
// nonsense in its configuration, so the manifest may not have any.
func alwaysSubstituted(m bundleManifest) []string {
	var loose []string
	for _, key := range m.references() {
		cfg, ok := m.UserConfig[key]
		if !ok {
			loose = append(loose, key+" (not declared)")
			continue
		}
		if !cfg.Required && cfg.Default == nil {
			loose = append(loose, key+" (optional, no default)")
		}
	}
	return loose
}

// repoRootPath is resolved while the package's variables are
// initialised, which is before any test body can change the working
// directory. Resolving it on demand instead would answer relative to
// wherever the last test left the process, and the pack tests run from
// the repository root.
var repoRootPath, repoRootErr = filepath.Abs(filepath.Join("..", ".."))

func repoRoot(t *testing.T) string {
	t.Helper()
	if repoRootErr != nil {
		t.Fatal(repoRootErr)
	}
	return repoRootPath
}

// resolveSchema loads a vendored JSON Schema ready to validate against.
func resolveSchema(t *testing.T, name string) (*jsonschema.Resolved, *jsonschema.Schema) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return resolved, &s
}

func readManifest(t *testing.T) (bundleManifest, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), manifestSource))
	if err != nil {
		t.Fatal(err)
	}
	var typed bundleManifest
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatal(err)
	}
	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatal(err)
	}
	return typed, loose
}

func TestBundleManifestMatchesTheSchema(t *testing.T) {
	_, loose := readManifest(t)
	// A floor on what was read: an empty document validates against a
	// schema whose fields are all optional, and would say nothing.
	if len(loose) < 10 {
		t.Fatalf("manifest has %d top-level fields, expected the full one", len(loose))
	}
	resolved, schema := resolveSchema(t, "mcpb-manifest-v0.3.schema.json")
	if len(schema.Properties) < 20 {
		t.Fatalf("vendored schema has %d properties, expected the published one", len(schema.Properties))
	}
	if err := resolved.Validate(loose); err != nil {
		t.Errorf("packaging/mcpb/manifest.json does not match the 0.3 schema: %v", err)
	}
}

func TestBundleManifestMatchesTheCode(t *testing.T) {
	m, _ := readManifest(t)

	if m.Name != server.Name {
		t.Errorf("manifest name %q, server name %q", m.Name, server.Name)
	}
	if m.Description != server.Description {
		t.Errorf("manifest description does not match server.Description:\n%q\n%q",
			m.Description, server.Description)
	}
	if m.Version != placeholderVersion {
		t.Errorf("manifest version %q, expected the placeholder %q that the pack script substitutes",
			m.Version, placeholderVersion)
	}
	if m.Server.Type != "binary" {
		t.Errorf("server type %q, expected binary", m.Server.Type)
	}

	// Every variable the bundle sets has to be one the server reads. A
	// misspelt name is silently ignored at runtime, so the bundle would
	// start and behave as though nothing had been configured.
	known := configVars()
	env := m.Server.MCPConfig.Env
	if len(env) == 0 {
		t.Fatal("the manifest sets no environment variables; this gate would check nothing")
	}
	for name := range env {
		if !slices.Contains(known, name) {
			t.Errorf("manifest sets %s, which this server does not read", name)
		}
	}

	if refs := m.references(); len(refs) == 0 {
		t.Error("the manifest substitutes no user configuration; the client secret should come from it")
	}
	if loose := alwaysSubstituted(m); len(loose) > 0 {
		t.Errorf("user configuration that the installing client may leave unsubstituted: %s",
			strings.Join(loose, ", "))
	}

	// All three platforms Claude Desktop runs on. A manifest cannot pick
	// a binary by architecture, so each platform it claims has to work on
	// both: macOS through the universal binary, Windows through amd64
	// under emulation, and Linux through a launcher that reads `uname -m`
	// and execs the right one of two binaries — Claude Desktop for Linux
	// ships x64 and arm64, so one Linux binary would be wrong for real
	// people rather than hypothetical ones.
	if got := m.Compatibility.Platforms; !slices.Equal(got, []string{"darwin", "linux", "win32"}) {
		t.Errorf("compatibility platforms %v, expected darwin, linux and win32", got)
	}
	if _, ok := m.Server.MCPConfig.PlatformOverrides["win32"]; !ok {
		t.Error("no win32 override: Windows needs the .exe named")
	}
	// Linux must not fall through to the default command, which is the
	// macOS universal binary.
	linux, ok := m.Server.MCPConfig.PlatformOverrides["linux"]
	if !ok {
		t.Fatal("no linux override: Linux would run the macOS binary")
	}
	if !strings.HasSuffix(linux.Command, "linux-launch.sh") {
		t.Errorf("linux command %q, expected the launcher — a single named binary "+
			"is wrong for one of the two architectures Claude Desktop for Linux supports",
			linux.Command)
	}
}

func TestAlwaysSubstitutedCatchesALooseReference(t *testing.T) {
	// The load-bearing case: a reference to a setting the person
	// installing can skip. Everything else in this table is the shape
	// that has to keep passing.
	const body = `{
      "server": {"mcp_config": {"command": "run", "env": {"A": "${user_config.%s}"}}},
      "user_config": {%s}
    }`
	tests := []struct {
		name   string
		key    string
		config string
		want   int
	}{
		{"required", "a", `"a": {"type": "string", "required": true}`, 0},
		{"defaulted", "a", `"a": {"type": "string", "default": ""}`, 0},
		{"optional, no default", "a", `"a": {"type": "string"}`, 1},
		{"not declared at all", "b", `"a": {"type": "string", "required": true}`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m bundleManifest
			if err := json.Unmarshal([]byte(fmt.Sprintf(body, tt.key, tt.config)), &m); err != nil {
				t.Fatal(err)
			}
			if got := alwaysSubstituted(m); len(got) != tt.want {
				t.Errorf("alwaysSubstituted = %v, want %d finding(s)", got, tt.want)
			}
		})
	}
}

// fakeDist is dist/ as goreleaser leaves it: the universal macOS binary,
// the Windows one and both Linux ones, in the directories their ids give
// them.
func fakeDist(t *testing.T, withDarwin, withWindows, withLinux bool) string {
	t.Helper()
	dist := t.TempDir()
	put := func(dir, name string) {
		if err := os.MkdirAll(filepath.Join(dist, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		// 0644 on purpose: the packer is what has to make the entry
		// executable, and it decides that from the name it gives the
		// entry rather than from the mode it found.
		if err := os.WriteFile(filepath.Join(dist, dir, name), []byte("binary"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withDarwin {
		put("universal_darwin_all", "google-chat-mcp")
	}
	if withWindows {
		put("binaries_windows_amd64_v1", "google-chat-mcp.exe")
	}
	if withLinux {
		put("binaries_linux_amd64_v1", "google-chat-mcp")
		put("binaries_linux_arm64_v8.0", "google-chat-mcp")
	}
	return dist
}

// runPack packs a bundle from the repository root, which is where
// goreleaser runs the hook.
//
// The working directory belongs to the process rather than to the test,
// so it is put back before this returns and nothing here runs in
// parallel.
func runPack(t *testing.T, dist string) (string, int) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot(t)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	}()

	var out bytes.Buffer
	code := mcpbPack([]string{"mcpb-pack", "v2.0.0", dist}, &out, &out)
	return out.String(), code
}

// bundleEntry is one file read back out of a bundle.
type bundleEntry struct {
	mode fs.FileMode
	body []byte
	time time.Time
}

// entries is a bundle read back, by the name each file takes inside it.
type entries map[string]bundleEntry

func readBundle(t *testing.T, path string) entries {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	out := entries{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Method != zip.Deflate {
			t.Errorf("%s is stored with method %d, not deflate", f.Name, f.Method)
		}
		out[f.Name] = bundleEntry{f.Mode(), body, f.Modified}
	}
	return out
}

func TestPackWritesWhatTheManifestNames(t *testing.T) {
	dist := fakeDist(t, true, true, true)
	out, code := runPack(t, dist)
	if code != 0 {
		t.Fatalf("pack failed (%d):\n%s", code, out)
	}

	bundle := filepath.Join(dist, "google-chat-mcp_2.0.0.mcpb")
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("the bundle was not written where checksums.txt will look for it: %v", err)
	}
	packed := readBundle(t, bundle)

	staged, ok := packed[manifestName]
	if !ok {
		t.Fatalf("no %s at the root of the bundle; the installer looks nowhere else", manifestName)
	}
	var m bundleManifest
	if err := json.Unmarshal(staged.body, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != "2.0.0" {
		t.Errorf("packed manifest version %q, want 2.0.0 from the tag", m.Version)
	}

	// The point of the whole test: whatever the manifest says it runs
	// has to be in the bundle, and executable.
	for platform, command := range m.commands() {
		rel, ok := strings.CutPrefix(command, dirnameRef)
		if !ok {
			t.Errorf("the %s command %q is not under %s", platform, command, dirnameRef)
			continue
		}
		entry, ok := packed[rel]
		if !ok {
			t.Errorf("the %s command runs %s, which the bundle does not carry", platform, rel)
			continue
		}
		if entry.mode.Perm()&0o111 == 0 {
			t.Errorf("%s is not executable in the bundle (%v)", rel, entry.mode.Perm())
		}
	}
	if m.Server.EntryPoint == "" {
		t.Fatal("the packed manifest declares no entry point")
	}
	if _, ok := packed[m.Server.EntryPoint]; !ok {
		t.Errorf("entry point %s is missing from the bundle", m.Server.EntryPoint)
	}
	// Linux is served by a launcher and two binaries, because a manifest
	// has no key for the architecture and Claude Desktop for Linux ships
	// both. All three have to be in the bundle or the override names a
	// file that is not there.
	for _, name := range []string{
		"server/linux-launch.sh", "server/google-chat-mcp-amd64", "server/google-chat-mcp-arm64",
	} {
		entry, ok := packed[name]
		if !ok {
			t.Errorf("%s is missing from the bundle", name)
			continue
		}
		if entry.mode.Perm()&0o111 == 0 {
			t.Errorf("%s is not executable in the bundle (%v); the source file's own mode is "+
				"not the answer, because the Windows build has no such bit to carry",
				name, entry.mode.Perm())
		}
	}
	for _, name := range []string{"LICENSE", "README.md"} {
		entry, ok := packed[name]
		if !ok {
			t.Errorf("%s is missing from the bundle", name)
			continue
		}
		if entry.mode.Perm()&0o111 != 0 {
			t.Errorf("%s is executable in the bundle (%v), and it is not something to run",
				name, entry.mode.Perm())
		}
	}
	// An unset time writes zeroes that display as the impossible
	// 1980-00-00, which is what a reader sees before they see anything
	// else about the bundle.
	for name, entry := range packed {
		if entry.time.IsZero() || entry.time.Year() < 1980 {
			t.Errorf("%s carries no usable modification time (%v)", name, entry.time)
		}
	}
}

// The same inputs have to make the same archive, or a checksum says
// nothing about whether two builds produced the same bundle.
func TestPackIsReproducible(t *testing.T) {
	dist := fakeDist(t, true, true, true)
	bundle := filepath.Join(dist, "google-chat-mcp_2.0.0.mcpb")

	if out, code := runPack(t, dist); code != 0 {
		t.Fatalf("pack failed (%d):\n%s", code, out)
	}
	first, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if out, code := runPack(t, dist); code != 0 {
		t.Fatalf("second pack failed (%d):\n%s", code, out)
	}
	second, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("two packs of the same tree differ (%d bytes then %d)", len(first), len(second))
	}
}

func TestPackRefusesAnIncompleteDist(t *testing.T) {
	tests := []struct {
		name                         string
		darwin, windows, linux, want bool
	}{
		{name: "no darwin binary", windows: true, linux: true},
		{name: "no windows binary", darwin: true, linux: true},
		{name: "no linux binaries", darwin: true, windows: true},
		{name: "all present", darwin: true, windows: true, linux: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := runPack(t, fakeDist(t, tt.darwin, tt.windows, tt.linux))
			if tt.want {
				if code != 0 {
					t.Fatalf("pack failed (%d):\n%s", code, out)
				}
				return
			}
			if code == 0 {
				t.Fatalf("packed a dist with a binary missing:\n%s", out)
			}
			if !strings.Contains(out, "expected exactly one") {
				t.Errorf("unhelpful failure:\n%s", out)
			}
		})
	}
}

// A glob that matched two would pack whichever sorted first, and the
// layout under dist/ carries a build id and an amd64 variant, so two is
// what a renamed build id looks like.
func TestPackRefusesTwoCandidates(t *testing.T) {
	dist := fakeDist(t, true, true, true)
	second := filepath.Join(dist, "other_linux_amd64_v3")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "google-chat-mcp"), []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runPack(t, dist)
	if code == 0 {
		t.Fatalf("packed with two linux amd64 candidates:\n%s", out)
	}
	if !strings.Contains(out, "found 2") {
		t.Errorf("the failure does not say what it found:\n%s", out)
	}
}

// The three things the published schema cannot check, each of which
// packs, installs cleanly, and then does nothing.
func TestCheckManifestHoldsTheManifestToTheTree(t *testing.T) {
	packed := []bundleFile{
		{name: "server/google-chat-mcp"},
		{name: "server/google-chat-mcp.exe"},
		{name: "server/linux-launch.sh"},
	}
	// good is the shape that has to keep passing, as JSON, so each case
	// below is one edit away from it.
	const good = `{
	  "server": {
	    "entry_point": "server/google-chat-mcp",
	    "mcp_config": {
	      "command": "${__dirname}/server/google-chat-mcp",
	      "env": {"GCM_CLIENT_SECRET": "${user_config.client_secret}"},
	      "platform_overrides": {
	        "linux": {"command": "${__dirname}/server/linux-launch.sh"},
	        "win32": {"command": "${__dirname}/server/google-chat-mcp.exe"}
	      }
	    }
	  },
	  "user_config": {"client_secret": {"type": "file", "required": true}}
	}`

	tests := []struct {
		name string
		edit func(string) string
		want string
	}{
		{name: "the shape that ships", edit: func(s string) string { return s }},
		{
			name: "an entry point nothing packs",
			edit: func(s string) string {
				return strings.Replace(s, `"entry_point": "server/google-chat-mcp"`,
					`"entry_point": "server/google-chat-mcp-x64"`, 1)
			},
			want: "entry_point names server/google-chat-mcp-x64",
		},
		{
			name: "no entry point at all",
			edit: func(s string) string {
				return strings.Replace(s, `"entry_point": "server/google-chat-mcp",`, "", 1)
			},
			want: "declares no entry_point",
		},
		{
			// The one a schema cannot see and a reader skims past: the
			// override is present, well formed, and names a file that is
			// not there. Windows installs the bundle and starts nothing.
			name: "a win32 override naming a file nothing packs",
			edit: func(s string) string {
				return strings.Replace(s, "server/google-chat-mcp.exe", "server/google-chat-mcp-amd64.exe", 1)
			},
			want: "the win32 command names server/google-chat-mcp-amd64.exe",
		},
		{
			name: "a linux override naming a file nothing packs",
			edit: func(s string) string {
				return strings.Replace(s, "server/linux-launch.sh", "server/launch.sh", 1)
			},
			want: "the linux command names server/launch.sh",
		},
		{
			name: "a command outside the bundle",
			edit: func(s string) string {
				return strings.Replace(s, "${__dirname}/server/google-chat-mcp\"",
					"/usr/local/bin/google-chat-mcp\"", 1)
			},
			want: "not under ${__dirname}",
		},
		{
			name: "a substitution user_config does not declare",
			edit: func(s string) string {
				return strings.Replace(s, "user_config.client_secret", "user_config.oauth_client", 1)
			},
			want: "${user_config.oauth_client} is substituted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m bundleManifest
			body := tt.edit(good)
			if err := json.Unmarshal([]byte(body), &m); err != nil {
				t.Fatalf("the edited manifest is not JSON: %v\n%s", err, body)
			}
			problems := checkManifest(m, packed)
			if tt.want == "" {
				if len(problems) > 0 {
					t.Fatalf("the shape that ships was reported: %s", strings.Join(problems, "; "))
				}
				return
			}
			if len(problems) != 1 {
				t.Fatalf("got %d problem(s), want 1: %s", len(problems), strings.Join(problems, "; "))
			}
			if !strings.Contains(problems[0], tt.want) {
				t.Errorf("problem\n got %q\nwant something containing %q", problems[0], tt.want)
			}
		})
	}
}
