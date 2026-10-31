package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/mmedum/google-chat-mcp/v9/internal/config"
	"github.com/mmedum/google-chat-mcp/v9/internal/server"
)

// The Claude Desktop bundle is built once a year at most, on a runner,
// from a manifest nobody reads in between. These tests are what stands
// between a typo in it and a release nobody can install: the manifest is
// checked against the published schema and against the code, and the
// pack script is run for real against a fake dist tree, so the paths the
// manifest names have to be the paths the bundle carries. Every check
// the packer runs is manifestProblems, and these tests run it too, on
// the staged names with no build: they are the gate.

// repoRootPath is resolved while the package's variables are
// initialized, which is before any test body can change the working
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

// The manifest validates against the vendored schema, and each side of
// that has a floor, so neither an emptied manifest nor a stub schema
// passes for the real thing.
func TestBundleManifestMatchesTheSchema(t *testing.T) {
	root := repoRoot(t)
	if got := schemaProblems(editedRaw(t, func(map[string]any) {}), root); len(got) != 0 {
		t.Errorf("the committed manifest: %q", got)
	}
	emptied := editedRaw(t, func(d map[string]any) { clear(d); d["name"] = server.Name })
	if got := schemaProblems(emptied, root); !slices.Equal(got,
		[]string{filepath.Join("packaging", "mcpb", "manifest.json") + " has 1 top-level fields, expected the full one"}) {
		t.Errorf("an emptied manifest gives %q", got)
	}
	stub := t.TempDir()
	put(t, stub, vendoredSchema, `{"type": "object", "properties": {"name": {"type": "string"}}}`)
	if got := schemaProblems(editedRaw(t, func(map[string]any) {}), stub); !slices.Equal(got,
		[]string{filepath.Join(stub, "scripts", "gates", "testdata", "mcpb-manifest-v0.3.schema.json") +
			" has 1 properties, expected the published one"}) {
		t.Errorf("a stub schema gives %q", got)
	}
}

// The committed manifest passes every check the packer runs, against
// the names the packer stages, read as the packer reads it: the bytes
// in the tree, not a decode of them, which would merge a repeated key.
func TestBundleManifestMatchesTheCode(t *testing.T) {
	_, problems, err := committedManifest(repoRoot(t), stagedNames())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// twice is the refusal of a key given twice in one object, at its path.
func twice(at string) string {
	return "the manifest has " + at + " more than once; the gate's decode merges a repeated key, " +
		"and the schema and the host keep the last, so they would read different documents"
}

// cased is the refusal of two keys in one object that differ only in
// case, at their paths.
func cased(first, second string) string {
	return "the manifest has " + first + " and " + second + " in one object; the gate's decode matches a key " +
		"to a field whatever its case, and the schema and the host do not, so they would read different documents"
}

// A key twice in one object is refused, whichever object it is in, and
// so are two that differ only in case, and nothing else is checked,
// since every other check would read a document the host does not.
func TestAManifestKeyGivenTwiceIsRefused(t *testing.T) {
	committed := readRepoFile(t, manifestSource)
	for _, tc := range []struct{ name, old, new, want string }{
		{"at the top", `"name": "google-chat-mcp",`, `"name": "google-chat-mcp", "name": "google-chat-mcp",`, twice("name")},
		{"an object", `"privacy_policies": [`, `"server": {"type": "binary"}, "privacy_policies": [`, twice("server")},
		{"in the environment", `"GCM_PROFILE": "${user_config.profile}",`,
			`"GCM_PROFILE": "${user_config.profile}", "GCM_PROFILE": "work",`, twice("server.mcp_config.env.GCM_PROFILE")},
		{"in another case", `"command": "${__dirname}/server/google-chat-mcp-darwin",`,
			`"command": "${__dirname}/server/google-chat-mcp-darwin", "Command": "${__dirname}/server/google-chat-mcp.exe",`,
			cased("server.mcp_config.command", "server.mcp_config.Command")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(committed, tc.old) {
				t.Fatalf("%s does not say %q", manifestSource, tc.old)
			}
			raw := []byte(strings.Replace(committed, tc.old, tc.new, 1))
			if got := manifestProblems(raw, stagedNames(), repoRoot(t)); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("problems %q, want %q alone", got, tc.want)
			}
		})
	}
}

// A key is repeated only within one object, and each repeat, or each key
// differing from an earlier one only in case, is named once, by a path
// that says which key it is: a list's elements and a repeated object's
// own keys included, and a key holding a dot or a bracket quoted. A
// document the walk cannot read is refused rather than passed.
func TestKeyProblemsNameEachPathOnce(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want []string
	}{
		{`{"a": [{"b": 1, "b": 2, "b": 3}], "c": {"d": 1}, "d": 1}`, []string{twice("a[0].b")}},
		{`{"x": {"y": 1}, "x": {"y": 1, "y": 2}}`, []string{twice("x"), twice("x.y")}},
		{`{"server": {"type": "binary"}, "user_config": {"type": {"type": "boolean"}}}`, nil},
		{`{"env": {"a.b": 1, "a": {"b": 2}, "a.b": 3}}`, []string{twice(`env["a.b"]`)}},
		{`{"a[0]": 1, "": 2, "a[0]": 3, "": 4}`, []string{twice(`["a[0]"]`), twice(`[""]`)}},
		{`{"server": {"command": 1, "Command": 2, "COMMAND": 3, "command": 4}}`, []string{
			cased("server.command", "server.Command"), cased("server.command", "server.COMMAND"),
			twice("server.command")}},
		{`{"\u212aey": 1, "key": 2}`, []string{cased("\u212aey", "key")}},
		{`{"a": 1,`, []string{"the manifest's keys cannot be read: unexpected end of JSON input"}},
	} {
		if got := keyProblems([]byte(tc.doc)); !slices.Equal(got, tc.want) {
			t.Errorf("keyProblems(%s) =\n%q\nwant\n%q", tc.doc, got, tc.want)
		}
	}
}

// compatibility.platforms is exactly the platforms the packer stages an
// entry point for, so a platform added to the staged table and not to
// the manifest is refused, and so is one dropped with its override.
func TestThePlatformsAreTheStagedOnes(t *testing.T) {
	m := editedManifest(t, func(map[string]any) {})
	if got := identityProblems(m, stagedPlatforms()); len(got) != 0 {
		t.Errorf("the committed manifest: %q", got)
	}
	want := "compatibility.platforms is [darwin linux win32]; the packer stages entry points for " +
		"[darwin freebsd linux win32]"
	if got := identityProblems(m, []string{"darwin", "freebsd", "linux", "win32"}); !slices.Equal(got, []string{want}) {
		t.Errorf("a platform the manifest does not claim: %q, want %q", got, want)
	}
}

// A platform the staged table gives two entry points is refused, rather
// than spawning whichever the table lists first.
func TestAPlatformGivenTwoEntryPointsIsRefused(t *testing.T) {
	saved := binaries
	t.Cleanup(func() { binaries = saved })
	binaries = append(slices.Clone(saved), staged{"*linux_riscv64*/google-chat-mcp",
		serverDir + "google-chat-mcp-linux-riscv64", "linux riscv64 binary", "linux"})
	want := "the packer stages server/launch-linux.sh and server/google-chat-mcp-linux-riscv64 as the entry " +
		"point for linux; a platform spawns one file"
	got := manifestProblems(editedRaw(t, func(map[string]any) {}), stagedNames(), repoRoot(t))
	if !slices.Contains(got, want) {
		t.Errorf("problems %q, want %q among them", got, want)
	}
	if platforms := stagedPlatforms(); !slices.Equal(platforms, []string{"darwin", "linux", "win32"}) {
		t.Errorf("stagedPlatforms = %q, want each platform once", platforms)
	}
	if file := entryPointFor("linux"); file != "server/launch-linux.sh" {
		t.Errorf("linux spawns %s, want the launcher, the table's first", file)
	}
}

// manifestProblems runs the checks the tests alone used to run: each
// one broken here is refused by the function the packer runs.
func TestManifestProblemsRunsEveryCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"a real version in the tree", func(d map[string]any) { d["version"] = "7.0.1" },
			`the manifest carries version "7.0.1"; the committed one carries the placeholder "0.0.0-dev"`},
		{"a branch for a schema", func(d map[string]any) {
			d["$schema"] = "https://raw.githubusercontent.com/anthropics/mcpb/main/schemas/mcpb-manifest-v0.3.schema.json"
		}, "does not name anthropics/mcpb at a tag"},
		{"a field the schema refuses", func(d map[string]any) { d["manifest_version"] = "0.2" },
			"does not match the 0.3 schema"},
		{"another server's name", func(d map[string]any) { d["name"] = "google-chat" },
			`manifest name "google-chat", server name "google-chat-mcp"`},
		{"another description", func(d map[string]any) { d["description"] = "Google Chat." },
			"manifest description does not match server.Description"},
		{"another kind of server", func(d map[string]any) { dig(d, "server")["type"] = "node" },
			`server type "node", expected binary`},
		{"an older Claude Desktop", func(d map[string]any) { dig(d, "compatibility")["claude_desktop"] = ">=0.9.0" },
			`compatibility.claude_desktop is ">=0.9.0", want the family's >=0.10.0`},
		{"two platforms", func(d map[string]any) { dig(d, "compatibility")["platforms"] = []any{"darwin", "win32"} },
			"compatibility.platforms is [darwin win32]; the packer stages entry points for [darwin linux win32]"},
		{"a field the person may skip", func(d map[string]any) {
			delete(dig(d, "user_config", "client_secret"), "default")
		}, "user configuration that the installing client may leave unsubstituted: client_secret (optional, no default)"},
		{"a command nothing stages", func(d map[string]any) {
			dig(d, "server", "mcp_config")["command"] = "${__dirname}/server/gone"
		}, "the default command names server/gone, which is not a program the bundle carries"},
		{"a setting turned on", func(d map[string]any) { dig(d, "user_config", "read_only")["default"] = true },
			"the bundle's defaults configure the server otherwise than no environment does, in ReadOnly;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := manifestProblems(editedRaw(t, tc.edit), stagedNames(), repoRoot(t))
			if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("problems:\n%s\nwant one containing %q", strings.Join(got, "\n"), tc.want)
			}
		})
	}
	// A reference user_config does not declare is one defect, reported
	// once.
	undeclared := manifestProblems(editedRaw(t, func(d map[string]any) {
		dig(d, "server", "mcp_config", "env")["GCM_NOT_READ"] = "${user_config.undeclared_key}"
	}), stagedNames(), repoRoot(t))
	if n := len(slices.DeleteFunc(slices.Clone(undeclared), func(p string) bool {
		return !strings.Contains(p, "undeclared_key")
	})); n != 1 {
		t.Errorf("an undeclared reference is reported %d times, want once:\n%s", n, strings.Join(undeclared, "\n"))
	}
	// The launcher and go.mod are read from the root it is given.
	root := copiedRoot(t)
	put(t, root, goModSource, "module example.com/elsewhere\n")
	got := manifestProblems(editedRaw(t, func(map[string]any) {}), stagedNames(), root)
	if want := `the launcher says "go install github.com/mmedum/google-chat-mcp/v9/cmd/google-chat-mcp@latest", ` +
		`want "go install example.com/elsewhere/cmd/google-chat-mcp@latest"`; !slices.Contains(got, want) {
		t.Errorf("problems %q, want %q among them", got, want)
	}
}

// copiedRoot is a repository holding the files the packer reads, copied
// from this one, for a test to break one of them.
func copiedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sources := []string{manifestSource, vendoredSchema, goModSource}
	for _, f := range carried {
		sources = append(sources, f.from)
	}
	for _, rel := range sources {
		put(t, root, rel, readRepoFile(t, rel))
	}
	return root
}

// put writes body to rel under root.
func put(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
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
func runPack(t *testing.T, dist string) (string, int) {
	t.Helper()
	return runPackIn(t, repoRoot(t), dist)
}

// runPackIn packs a bundle from root.
//
// The working directory belongs to the process rather than to the test,
// so it is put back before this returns and nothing here runs in
// parallel.
func runPackIn(t *testing.T, root, dist string) (string, int) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
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
	files, err := bundleLayout(dist)
	if err != nil {
		t.Fatal(err)
	}
	linux := []string{launcherName}
	for _, f := range files {
		if strings.HasPrefix(f.name, serverDir+server.Name+"-linux-") {
			linux = append(linux, f.name)
		}
	}
	if len(linux) != 3 {
		t.Errorf("the packer stages %v for Linux, want the launcher and two binaries", linux)
	}
	for _, name := range linux {
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

// The packer refuses what the tests refuse, because both run
// manifestProblems: a name only the tests used to check stops the pack,
// and so does a launcher the manifest never mentions.
func TestPackRunsEveryCheckTheGateRuns(t *testing.T) {
	for _, tc := range []struct {
		name, file, old, new, want string
	}{
		{"another server's name", manifestSource, `"name": "google-chat-mcp"`, `"name": "google-chat"`,
			`manifest name "google-chat", server name "google-chat-mcp"`},
		{"a key twice", manifestSource, `"name": "google-chat-mcp",`, `"name": "google-chat-mcp", "name": "google-chat-mcp",`,
			"the manifest has name more than once"},
		{"a launcher naming another binary", launcherSource, "google-chat-mcp-linux-arm64", "google-chat-mcp-linux-aarch64",
			"the launcher runs [google-chat-mcp-linux-aarch64 google-chat-mcp-linux-amd64] and the packer stages " +
				"[google-chat-mcp-linux-amd64 google-chat-mcp-linux-arm64]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := copiedRoot(t)
			if out, code := runPackIn(t, root, fakeDist(t, true, true, true)); code != 0 {
				t.Fatalf("the copied tree does not pack (%d):\n%s", code, out)
			}
			body := readRepoFile(t, tc.file)
			if !strings.Contains(body, tc.old) {
				t.Fatalf("%s does not say %q", tc.file, tc.old)
			}
			put(t, root, tc.file, strings.ReplaceAll(body, tc.old, tc.new))
			dist := fakeDist(t, true, true, true)
			out, code := runPackIn(t, root, dist)
			if code == 0 || !strings.Contains(out, tc.want) {
				t.Errorf("pack exited %d:\n%s\nwant it refused with %q", code, out, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dist, "google-chat-mcp_2.0.0.mcpb")); err == nil {
				t.Error("a refused pack still wrote the bundle")
			}
		})
	}
}

// failingWriter refuses every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// The end of a bundle reaches the disk only when it closes, so a close
// that fails fails the pack: the zip's, which writes the directory an
// installer reads first, and the file's, which leaves nothing at the
// bundle's name.
func TestABundleThatDoesNotCloseIsNotWritten(t *testing.T) {
	source := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(source, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Small enough that the zip writer buffers every entry, so the
	// writes fail first at its close.
	err := writeEntries(failingWriter{}, []bundleFile{{name: serverDir + "x", from: source}}, []byte("{}"))
	if err == nil || err.Error() != "disk full" {
		t.Errorf("writing through a failing disk gave %v, want the disk's error", err)
	}

	out := filepath.Join(t.TempDir(), "b.mcpb")
	err = writeThrough(out, func(w io.Writer) error {
		// Closed early, so writeThrough's own close is the one that fails.
		return w.(*os.File).Close()
	})
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("a failed close gave %v, want os.ErrClosed", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a bundle that did not close is at %s: %v", out, statErr)
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
	packed, err := bundleLayout(fakeDist(t, true, true, true))
	if err != nil {
		t.Fatal(err)
	}
	overrides := []string{"server", "mcp_config", "platform_overrides"}
	tests := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{name: "the shape that ships", edit: func(map[string]any) {}},
		{
			name: "an entry point nothing packs",
			edit: func(d map[string]any) { dig(d, "server")["entry_point"] = "server/google-chat-mcp-x64" },
			want: "entry_point names server/google-chat-mcp-x64",
		},
		{
			name: "no entry point at all",
			edit: func(d map[string]any) { delete(dig(d, "server"), "entry_point") },
			want: "declares no entry_point",
		},
		{
			// The one a schema cannot see and a reader skims past: the
			// override is present, well formed, and names a file that is
			// not there. Windows installs the bundle and starts nothing.
			name: "a win32 override naming a file nothing packs",
			edit: func(d map[string]any) {
				dig(d, append(overrides, "win32")...)["command"] = "${__dirname}/server/google-chat-mcp-amd64.exe"
			},
			want: "the win32 command names server/google-chat-mcp-amd64.exe",
		},
		{
			name: "a linux override naming a file nothing packs",
			edit: func(d map[string]any) {
				dig(d, append(overrides, "linux")...)["command"] = "${__dirname}/server/launch.sh"
			},
			want: "the linux command names server/launch.sh",
		},
		{
			name: "a command outside the bundle",
			edit: func(d map[string]any) { dig(d, "server", "mcp_config")["command"] = "/usr/local/bin/google-chat-mcp" },
			want: `the default command is "/usr/local/bin/google-chat-mcp"; a command starts with ${__dirname}/`,
		},
		{
			// The specification shows this form once and never says what
			// it is resolved against.
			name: "a relative command",
			edit: func(d map[string]any) { dig(d, "server", "mcp_config")["command"] = "server/google-chat-mcp-darwin" },
			want: `the default command is "server/google-chat-mcp-darwin"; a command starts with ${__dirname}/`,
		},
		{
			name: "an override's absolute command",
			edit: func(d map[string]any) {
				dig(d, append(overrides, "win32")...)["command"] = "/server/google-chat-mcp.exe"
			},
			want: `the win32 command is "/server/google-chat-mcp.exe"; a command starts with ${__dirname}/`,
		},
		{
			// Packed, and not a program: the README is there to be read.
			name: "a command naming the README",
			edit: func(d map[string]any) { dig(d, "server", "mcp_config")["command"] = "${__dirname}/README.md" },
			want: "the default command names README.md, which is not a program the bundle carries",
		},
		{
			name: "an override naming the license",
			edit: func(d map[string]any) { dig(d, append(overrides, "linux")...)["command"] = "${__dirname}/LICENSE" },
			want: "the linux command names LICENSE, which is not a program the bundle carries",
		},
		{
			name: "an entry point naming the license",
			edit: func(d map[string]any) { dig(d, "server")["entry_point"] = "LICENSE" },
			want: "entry_point names LICENSE, which is not a program the bundle carries",
		},
		{
			// A program, and not the one the default command runs.
			name: "an entry point the default command does not run",
			edit: func(d map[string]any) { dig(d, "server")["entry_point"] = "server/google-chat-mcp.exe" },
			want: "entry_point names server/google-chat-mcp.exe, and the default command runs server/google-chat-mcp-darwin",
		},
		{
			// Staged, and a program, and the file staged for another
			// platform: macOS would spawn a Windows binary. The entry
			// point moves with the command, so this is the one defect.
			name: "macOS spawning the Windows binary",
			edit: func(d map[string]any) {
				dig(d, "server", "mcp_config")["command"] = "${__dirname}/server/google-chat-mcp.exe"
				dig(d, "server")["entry_point"] = "server/google-chat-mcp.exe"
			},
			want: `on darwin the bundle would spawn "server/google-chat-mcp.exe", and the file staged for darwin is ` +
				`"server/google-chat-mcp-darwin"`,
		},
		{
			name: "Windows spawning the macOS binary",
			edit: func(d map[string]any) {
				dig(d, append(overrides, "win32")...)["command"] = "${__dirname}/server/google-chat-mcp-darwin"
			},
			want: `on win32 the bundle would spawn "server/google-chat-mcp-darwin", and the file staged for win32 is ` +
				`"server/google-chat-mcp.exe"`,
		},
		{
			// Windows then runs the default command, the macOS binary.
			name: "no win32 override",
			edit: func(d map[string]any) { delete(dig(d, overrides...), "win32") },
			want: `on win32 the bundle would spawn "server/google-chat-mcp-darwin"`,
		},
		{
			name: "no linux override",
			edit: func(d map[string]any) { delete(dig(d, overrides...), "linux") },
			want: `on linux the bundle would spawn "server/google-chat-mcp-darwin"`,
		},
		{
			// Right for one of the two architectures Claude Desktop for
			// Linux ships.
			name: "one Linux binary for both architectures",
			edit: func(d map[string]any) {
				dig(d, append(overrides, "linux")...)["command"] = "${__dirname}/server/google-chat-mcp-linux-amd64"
			},
			want: `on linux the bundle would spawn "server/google-chat-mcp-linux-amd64", and the file staged for linux is ` +
				`"server/launch-linux.sh"`,
		},
		{
			name: "an override for a platform the bundle does not claim",
			edit: func(d map[string]any) {
				dig(d, overrides...)["freebsd"] = map[string]any{"command": "${__dirname}/server/google-chat-mcp-linux-amd64"}
			},
			want: "platform_overrides.freebsd is an override for a platform compatibility.platforms does not claim",
		},
		{
			name: "a claimed platform the packer stages nothing for",
			edit: func(d map[string]any) {
				dig(d, "compatibility")["platforms"] = []any{"darwin", "freebsd", "linux", "win32"}
			},
			want: "compatibility.platforms claims freebsd and the packer stages no entry point for it",
		},
		{
			name: "a substitution user_config does not declare",
			edit: func(d map[string]any) {
				dig(d, "server", "mcp_config", "env")["GCM_CLIENT_SECRET"] = "${user_config.oauth_client}"
			},
			want: "${user_config.oauth_client} is substituted",
		},
		{
			name: "a substitution user_config does not declare, in an argument",
			edit: func(d map[string]any) { dig(d, "server", "mcp_config")["args"] = []any{"${user_config.verbose}"} },
			want: "${user_config.verbose} is substituted",
		},
		{
			name: "a substitution user_config does not declare, in an override's argument",
			edit: func(d map[string]any) {
				dig(d, append(overrides, "win32")...)["args"] = []any{"--profile=${user_config.account}"}
			},
			want: "${user_config.account} is substituted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := checkManifest(editedManifest(t, tt.edit), packed)
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

// A $schema at a branch is a claim about a document that can be amended
// under it. Every other claim here holds the manifest against itself or
// against the tree; this one holds it against a fixed thing upstream.
func TestSchemaRefMustNameATag(t *testing.T) {
	const base = "https://raw.githubusercontent.com/anthropics/mcpb/"
	const file = "/schemas/mcpb-manifest-v0.3.schema.json"
	for _, tc := range []struct {
		name, schema, version, want string
	}{
		{
			name:    "a tag, naming the format the manifest declares",
			schema:  base + "v2.1.2" + file,
			version: "0.3",
		},
		{
			name:    "a branch",
			schema:  base + "main" + file,
			version: "0.3",
			want:    "does not name anthropics/mcpb at a tag",
		},
		{
			name:    "a partial tag, which is a moving target too",
			schema:  base + "v2.1" + file,
			version: "0.3",
			want:    "does not name anthropics/mcpb at a tag",
		},
		{
			name:    "somewhere else entirely",
			schema:  "https://example.com/mcpb-manifest-v0.3.schema.json",
			version: "0.3",
			want:    "does not name anthropics/mcpb at a tag",
		},
		{
			name: "no schema at all",
			want: "names no $schema",
		},
		{
			// The claim the vendored copy cannot make: it pins
			// manifest_version by const, so the manifest cannot drift
			// from the copy — but nothing asks the copy about the URL.
			name:    "a schema for one format, a manifest declaring another",
			schema:  base + "v2.1.2/schemas/mcpb-manifest-v0.4.schema.json",
			version: "0.3",
			want:    "claims to be one format and points at another",
		},
		{
			name:   "a tagged ref with no manifest_version beside it",
			schema: base + "v2.1.2" + file,
			want:   "declares no manifest_version",
		},
		{
			name:    "a tagged ref that names no schema file",
			schema:  base + "v2.1.2/schemas/something-else.json",
			version: "0.3",
			want:    "does not name a manifest schema file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m bundleManifest
			m.Schema = tc.schema
			m.ManifestVersion = tc.version
			got := schemaRefProblem(m)
			if tc.want == "" {
				if got != "" {
					t.Errorf("a good $schema was refused: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("problem = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// The repository's own manifest has to pass, or the rule is aspirational.
func TestTheRealManifestNamesATaggedSchema(t *testing.T) {
	var m bundleManifest
	if err := json.Unmarshal([]byte(readRepoFile(t, manifestSource)), &m); err != nil {
		t.Fatalf("%s: %v", manifestSource, err)
	}
	if p := schemaRefProblem(m); p != "" {
		t.Error(p)
	}
}

// The vendored schema is the fixture every version claim leans on: the
// manifest is held to manifest_version "0.3" by that copy's own `const`,
// through a real schema library, which is a stronger pin than a string
// comparison here would be. What it cannot catch is the document and the
// copy being moved down together — 0.2 beside a 0.2 schema agrees with
// itself. So the copy gets a floor, the way the property count does.
func TestTheVendoredSchemaIsNotBelowTheFormatWeTarget(t *testing.T) {
	const floorMajor, floorMinor = 0, 3

	var schema struct {
		Properties struct {
			ManifestVersion struct {
				Const string `json:"const"`
			} `json:"manifest_version"`
		} `json:"properties"`
	}
	raw := readRepoFile(t, filepath.Join("scripts", "gates", "testdata", "mcpb-manifest-v0.3.schema.json"))
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("vendored schema: %v", err)
	}

	got := schema.Properties.ManifestVersion.Const
	major, minor, ok := strings.Cut(got, ".")
	if !ok {
		t.Fatalf("vendored schema pins manifest_version to %q, which is not a version: "+
			"the copy is not the one every other claim assumes", got)
	}
	gotMajor, err := strconv.Atoi(major)
	if err != nil {
		t.Fatalf("vendored schema manifest_version major %q: %v", major, err)
	}
	gotMinor, err := strconv.Atoi(minor)
	if err != nil {
		t.Fatalf("vendored schema manifest_version minor %q: %v", minor, err)
	}
	if gotMajor < floorMajor || (gotMajor == floorMajor && gotMinor < floorMinor) {
		t.Errorf("vendored schema pins manifest_version to %q, below the %d.%d this server targets: "+
			"a manifest moved down with the copy it validates against still agrees with itself",
			got, floorMajor, floorMinor)
	}
}

// editedManifest is the committed manifest with one edit applied, read
// back the way the gate reads it.
func editedManifest(t *testing.T, edit func(map[string]any)) bundleManifest {
	t.Helper()
	var m bundleManifest
	if err := json.Unmarshal(editedRaw(t, edit), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// editedRaw is the committed manifest with one edit applied, encoded.
func editedRaw(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	_, doc := readManifest(t)
	edit(doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// dig is the object at a path in a decoded manifest.
func dig(doc map[string]any, path ...string) map[string]any {
	for _, key := range path {
		doc = doc[key].(map[string]any)
	}
	return doc
}

// Each rule settingProblems keeps, broken once in the committed
// manifest, and the shape that ships passing.
func TestSettingProblemsRefuseEachBreak(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{name: "the shape that ships", edit: func(map[string]any) {}},
		{"no privacy policy", func(d map[string]any) { delete(d, "privacy_policies") }, "privacy_policies is empty"},
		{"an empty privacy policy list", func(d map[string]any) { d["privacy_policies"] = []any{} },
			"privacy_policies is empty"},
		{"a switch the bundle turns on", func(d map[string]any) {
			dig(d, "user_config", "enable_destructive")["default"] = true
		}, "the bundle's defaults configure the server otherwise than no environment does, in EnableDestructive;"},
		{"a required switch the bundle turns on, which Claude Desktop fills in", func(d map[string]any) {
			dig(d, "user_config", "enable_destructive")["default"] = true
			dig(d, "user_config", "enable_destructive")["required"] = true
		}, "the bundle's defaults configure the server otherwise than no environment does, in EnableDestructive;"},
		{"a required field with a list for a default", func(d map[string]any) {
			dig(d, "user_config", "client_secret")["default"] = []any{"client.json"}
		}, "user_config.client_secret defaults to a list, which the defaults check cannot build an environment from"},
		{"a switch asked as text", func(d map[string]any) {
			dig(d, "user_config", "read_only")["type"] = "string"
			dig(d, "user_config", "read_only")["default"] = "false"
		}, "user_config.read_only is a string; GCM_READ_ONLY is a switch, so ask for a boolean"},
		{"a switch with no default", func(d map[string]any) {
			delete(dig(d, "user_config", "read_only"), "default")
		}, "the environment the bundle's defaults give the server is refused: config: invalid: read-only " +
			`"${user_config.read_only}"`},
		{"an override's env, which replaces the base", func(d map[string]any) {
			dig(d, "server", "mcp_config", "platform_overrides", "win32")["env"] = map[string]any{
				"GCM_ENABLE_DESTRUCTIVE": "true",
			}
		}, "platform_overrides.win32.env is set"},
		{"an empty override env, which replaces the base too", func(d map[string]any) {
			dig(d, "server", "mcp_config", "platform_overrides", "linux")["env"] = map[string]any{}
		}, "platform_overrides.linux.env is set"},
		{"no argument list in mcp_config", func(d map[string]any) {
			delete(dig(d, "server", "mcp_config"), "args")
		}, "mcp_config.args is missing; keep it as []"},
		{"a null argument list in mcp_config", func(d map[string]any) {
			dig(d, "server", "mcp_config")["args"] = nil
		}, "mcp_config.args is null; keep it as []"},
		{"an argument that turns the deletes on", func(d map[string]any) {
			dig(d, "server", "mcp_config")["args"] = []any{"--enable-destructive=true"}
		}, `mcp_config.args is ["--enable-destructive=true"]`},
		{"an override's argument, which replaces the base", func(d map[string]any) {
			dig(d, "server", "mcp_config", "platform_overrides", "linux")["args"] = []any{"--enable-destructive=true"}
		}, "platform_overrides.linux.args is set"},
		{"an empty override argument list, which replaces the base too", func(d map[string]any) {
			dig(d, "server", "mcp_config", "platform_overrides", "win32")["args"] = []any{}
		}, "platform_overrides.win32.args is set"},
		{"a toolset the description leaves out", func(d map[string]any) {
			dig(d, "user_config", "toolsets")["description"] = "Comma-separated tool groups to register, or all. " +
				"The groups are core, sections, pins, emoji, events, readstate, settings, availability."
		}, `user_config.toolsets does not list the toolsets as ` +
			`"core, sections, pins, emoji, events, readstate, settings, availability, admin"`},
		{"every toolset's name, only as words in the prose", func(d map[string]any) {
			dig(d, "user_config", "toolsets")["description"] = "Pick groups, or all: core is always on, and admin " +
				"needs an administrator. The rest are sections, pins, emoji, events, readstate, settings and availability."
		}, `user_config.toolsets does not list the toolsets as ` +
			`"core, sections, pins, emoji, events, readstate, settings, availability, admin"`},
		{"a setting the bundle no longer asks for", func(d map[string]any) {
			delete(dig(d, "user_config"), "local_dir")
			delete(dig(d, "server", "mcp_config", "env"), "GCM_LOCAL_DIR")
		}, "user_config does not ask for local_dir, which sets GCM_LOCAL_DIR"},
		{"a setting spent under another key", func(d map[string]any) {
			dig(d, "server", "mcp_config", "env")["GCM_PROFILE"] = "${user_config.toolsets}"
		}, `mcp_config.env.GCM_PROFILE is "${user_config.toolsets}", want "${user_config.profile}"`},
		{"the destructive switch's removed name", func(d map[string]any) {
			dig(d, "server", "mcp_config", "env")["GCM_ALLOW_DESTRUCTIVE"] = "${user_config.enable_destructive}"
		}, "mcp_config.env sets GCM_ALLOW_DESTRUCTIVE, which a release removed"},
		{"the destructive switch's old key", func(d map[string]any) {
			uc := dig(d, "user_config")
			uc["allow_destructive"] = uc["enable_destructive"]
			delete(uc, "enable_destructive")
			dig(d, "server", "mcp_config", "env")["GCM_ENABLE_DESTRUCTIVE"] = "${user_config.allow_destructive}"
		}, "user_config does not ask for enable_destructive"},
		{"a setting the bundle leaves out", func(d map[string]any) {
			dig(d, "server", "mcp_config", "env")["GCM_REFRESH_TOKEN"] = "x"
		}, "mcp_config.env sets GCM_REFRESH_TOKEN, which the bundle leaves out: a credential for automation"},
		{"a variable the server does not read", func(d map[string]any) {
			dig(d, "server", "mcp_config", "env")["GCM_TOOLSET"] = "${user_config.toolsets}"
		}, "mcp_config.env sets GCM_TOOLSET, which the server does not read"},
		{"a removed setting", func(d map[string]any) {
			dig(d, "server", "mcp_config", "env")["GCM_REQUIRE_PROMPT"] = "false"
		}, "mcp_config.env sets GCM_REQUIRE_PROMPT, which a release removed"},
		{"a default that configures something", func(d map[string]any) {
			dig(d, "user_config", "toolsets")["default"] = "all,admin"
		}, "the bundle's defaults configure the server otherwise than no environment does, in Toolsets;"},
		{"two defaults that configure something", func(d map[string]any) {
			dig(d, "user_config", "read_only")["default"] = true
			dig(d, "user_config", "profile")["default"] = "work"
		}, "the bundle's defaults configure the server otherwise than no environment does, in Profile, ReadOnly;"},
		{"a profile field that does not say what a name is", func(d map[string]any) {
			dig(d, "user_config", "profile")["description"] = "Which stored login to use."
		}, `user_config.profile does not say a profile name is "up to 64 characters from a to z, 0 to 9, - and _, ` +
			`starting with a letter or digit"`},
		{"an optional setting with no default", func(d map[string]any) {
			delete(dig(d, "user_config", "profile"), "default")
		}, "the environment the bundle's defaults give the server is refused: config: invalid: profile " +
			`"${user_config.profile}"`},
		{"a key no setting has", func(d map[string]any) {
			dig(d, "user_config")["verbose"] = map[string]any{"type": "boolean", "title": "t", "description": "d"}
		}, "user_config asks for verbose, which is no setting's key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := settingProblems(editedManifest(t, tc.edit), bundleLeftOut)
			if tc.want == "" {
				if len(problems) > 0 {
					t.Fatalf("the shape that ships was reported: %s", strings.Join(problems, "; "))
				}
				return
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("problems %q\nwant one containing %q", problems, tc.want)
			}
		})
	}
}

// Every default the check cannot read is reported, in key order, and
// the comparison still runs over the rest, so one run names everything
// wrong with the defaults.
func TestDefaultsProblemsReportsEveryUnreadableDefaultAndTheRest(t *testing.T) {
	m := editedManifest(t, func(d map[string]any) {
		uc := dig(d, "user_config")
		dig(uc, "profile")["default"] = map[string]any{"name": "work"}
		dig(uc, "local_dir")["default"] = []any{"/tmp"}
		dig(uc, "client_secret")["default"] = []any{"client.json"}
		dig(uc, "enable_destructive")["default"] = true
	})
	const cannot = ", which the defaults check cannot build an environment from: give it a boolean, a string or a number"
	want := []string{
		"user_config.client_secret defaults to a list" + cannot,
		"user_config.local_dir defaults to a list" + cannot,
		"user_config.profile defaults to an object" + cannot,
	}
	got, _ := defaultsProblems(m)
	if len(got) != 4 || !slices.Equal(got[:3], want) ||
		!strings.HasPrefix(got[3], "the bundle's defaults configure the server otherwise than no environment does, in EnableDestructive;") {
		t.Errorf("defaultsProblems =\n%s\nwant\n%s\nand then the comparison", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The bundle asks for every variable the server reads, less the
// removed ones and less each one left out with a reason, so
// a setting added later is a decision about the bundle rather than a
// gap in it. An exclusion the server has no use for is refused.
func TestBundleVarsAreDerivedFromTheSettings(t *testing.T) {
	if read := config.EnvVars(); len(read) < 20 {
		t.Fatalf("the server reads %d variables; this would be deriving from a stub", len(read))
	}
	asked, problems := bundleVarsFrom(
		[]string{"GCM_ASKED", "GCM_LEFT_OUT", "GCM_REMOVED", "GCM_ADDED_LATER"},
		map[string]config.Var{"GCM_ASKED": {Name: "GCM_ASKED"}, "GCM_REMOVED": {Name: "GCM_REMOVED", Removed: true}},
		map[string]string{"GCM_LEFT_OUT": "a reason", "GCM_GONE": "a reason", "GCM_REMOVED": "a reason"},
	)
	if want := []string{"GCM_ASKED", "GCM_ADDED_LATER"}; !slices.Equal(asked, want) {
		t.Errorf("asked = %v, want %v", asked, want)
	}
	want := []string{
		"bundleLeftOut names GCM_GONE, which the server does not read",
		"bundleLeftOut names GCM_REMOVED, which a release removed, and so left out already",
	}
	if !slices.Equal(problems, want) {
		t.Errorf("problems = %q, want %q", problems, want)
	}
}

// A stale exclusion fails the gate itself, not only the derivation.
func TestSettingProblemsRefuseAStaleExclusion(t *testing.T) {
	leftOut := maps.Clone(bundleLeftOut)
	leftOut["GCM_GONE"] = "a reason"
	m, _ := readManifest(t)
	want := "bundleLeftOut names GCM_GONE, which the server does not read"
	if got := settingProblems(m, leftOut); !slices.Contains(got, want) {
		t.Errorf("settingProblems = %q, want %q among them", got, want)
	}
}

// The launcher picks between binaries no manifest names, so renaming a
// staged one would pass every check above and ship a bundle that starts
// nothing on Linux. Its install hint names the module path, which moved
// at v6 and was left behind once; go.mod is read under Windows line
// endings too.
func TestTheLauncherRunsWhatThePackerStages(t *testing.T) {
	files := stagedNames()
	if got := launcherProblems(files, repoRoot(t)); len(got) != 0 {
		t.Errorf("the committed launcher: %q", got)
	}
	launcher, goMod := readRepoFile(t, launcherSource), readRepoFile(t, goModSource)
	const module = "github.com/mmedum/google-chat-mcp/v9"
	if !strings.Contains(launcher, "go install "+module+"/cmd/google-chat-mcp@latest") {
		t.Fatalf("the launcher's install hint does not name %s", module)
	}
	for _, tc := range []struct {
		name, launcher, goMod string
		want                  []string
	}{
		{"go.mod with Windows line endings", launcher, strings.ReplaceAll(goMod, "\n", "\r\n"), nil},
		{"a renamed binary", strings.ReplaceAll(launcher, "google-chat-mcp-linux-amd64", "google-chat-mcp-linux-x64"), goMod,
			[]string{"the launcher runs [google-chat-mcp-linux-arm64 google-chat-mcp-linux-x64] and the packer stages " +
				"[google-chat-mcp-linux-amd64 google-chat-mcp-linux-arm64]"}},
		{"a hint left at the last major version", strings.ReplaceAll(launcher, module+"/", "github.com/mmedum/google-chat-mcp/v6/"), goMod,
			[]string{`the launcher says "go install github.com/mmedum/google-chat-mcp/v6/cmd/google-chat-mcp@latest", ` +
				`want "go install ` + module + `/cmd/google-chat-mcp@latest"`}},
		{"no hint", strings.ReplaceAll(launcher, "go install", "install"), goMod,
			[]string{"the launcher gives no install hint for an architecture the bundle does not carry"}},
		{"no module", launcher, "go 1.27\n", []string{"go.mod names no module"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, launcherSource, tc.launcher)
			put(t, root, goModSource, tc.goMod)
			if got := launcherProblems(files, root); !slices.Equal(got, tc.want) {
				t.Errorf("launcherProblems = %q, want %q", got, tc.want)
			}
		})
	}
}

// A default holding a ${...} reference is refused, naming the key: the
// host may expand it, and the defaults check does not.
func TestADefaultHoldingAReferenceIsRefused(t *testing.T) {
	for _, tc := range []struct{ def, want string }{
		{"${HOME}/chat", `user_config.local_dir defaults to "${HOME}/chat"; a default holds a value, not a ${...} ` +
			`reference, which the host may expand`},
		{"${user_config.profile}", `user_config.local_dir defaults to "${user_config.profile}"; a default holds a ` +
			`value, not a ${...} reference, which the host may expand`},
	} {
		m := editedManifest(t, func(d map[string]any) { dig(d, "user_config", "local_dir")["default"] = tc.def })
		if got := settingProblems(m, bundleLeftOut); !slices.Contains(got, tc.want) {
			t.Errorf("a default of %q gives %q, want %q among them", tc.def, got, tc.want)
		}
	}
	if got := settingProblems(editedManifest(t, func(map[string]any) {}), bundleLeftOut); len(got) != 0 {
		t.Errorf("the committed manifest: %q", got)
	}
}

// modulePath reads the module directive as Go writes it: after spaces
// or a tab, quoted or not, with a comment after it or a line before it,
// under Windows line endings too.
func TestModulePathReadsTheDirective(t *testing.T) {
	for _, tc := range []struct{ goMod, want string }{
		{"module example.com/a\n\ngo 1.27\n", "example.com/a"},
		{"module\texample.com/a\n", "example.com/a"},
		{"module   example.com/a   // the module\n", "example.com/a"},
		{"module \"example.com/a\"\n", "example.com/a"},
		{"// module example.com/b\r\nmodule example.com/a\r\ngo 1.27\r\n", "example.com/a"},
		{"go 1.27\n", "go.mod names no module"},
	} {
		root := t.TempDir()
		put(t, root, goModSource, tc.goMod)
		got, err := modulePath(root)
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("modulePath of %q = %q, want %q", tc.goMod, got, tc.want)
		}
	}
}

// The profile and toolsets fields are held to what the server accepts.
// A bundle that drops the profile field is told so once, and one that
// leaves either setting out is refused, so the field check cannot pass
// in silence.
func TestTheFieldRulesHaveAFloor(t *testing.T) {
	dropped := editedManifest(t, func(d map[string]any) {
		delete(dig(d, "user_config"), "profile")
		delete(dig(d, "server", "mcp_config", "env"), "GCM_PROFILE")
	})
	if p := slices.DeleteFunc(settingProblems(dropped, bundleLeftOut), func(p string) bool {
		return !strings.Contains(p, "GCM_PROFILE")
	}); !slices.Equal(p, []string{"user_config does not ask for profile, which sets GCM_PROFILE"}) {
		t.Errorf("a dropped profile field gives %q, want the one problem", p)
	}
	for name, words := range map[string]string{
		"GCM_PROFILE":  "up to 64 characters from a to z, 0 to 9, - and _, starting with a letter or digit",
		"GCM_TOOLSETS": "core, sections, pins, emoji, events, readstate, settings, availability, admin",
	} {
		leftOut := maps.Clone(bundleLeftOut)
		leftOut[name] = "left out"
		m := editedManifest(t, func(d map[string]any) {
			key := bundleKey(name)
			delete(dig(d, "user_config"), key)
			delete(dig(d, "server", "mcp_config", "env"), name)
		})
		want := fmt.Sprintf("the bundle asks for no %s, so nothing holds its field to %q", name, words)
		if got := settingProblems(m, leftOut); !slices.Contains(got, want) {
			t.Errorf("a bundle with no %s gives %q, want %q among them", name, got, want)
		}
	}
}

// A field the defaults change is named, an unexported one included
// where its type compares by what it holds; an unexported one that does
// not is reported unnamed whenever it differs, whatever else does,
// rather than panicking. A pointer is compared by what it points to,
// never by its address.
func TestTheConfigDiffNamesTheFieldsThatDiffer(t *testing.T) {
	type cfg struct {
		Shown  int
		hidden int
		rest   []string
		held   any
		at     *int
	}
	one, otherOne, two := 1, 1, 2
	base := cfg{Shown: 1, hidden: 1, at: &one}
	for _, tc := range []struct {
		b    cfg
		want []string
	}{
		{cfg{Shown: 2, hidden: 1, at: &one}, []string{"Shown"}},
		{cfg{Shown: 1, hidden: 2, at: &one}, []string{"hidden"}},
		{cfg{Shown: 1, hidden: 1, rest: []string{"x"}, at: &one}, []string{"an unexported field"}},
		{cfg{Shown: 2, hidden: 2, rest: []string{"x"}, at: &one}, []string{"Shown", "hidden", "an unexported field"}},
		{cfg{Shown: 1, hidden: 1, at: &otherOne}, nil},
		{cfg{Shown: 1, hidden: 1, at: &two}, []string{"an unexported field"}},
		{cfg{Shown: 1, hidden: 1}, []string{"an unexported field"}},
		{cfg{Shown: 1, hidden: 1, at: &one}, nil},
	} {
		if got := fieldDiff(base, tc.b); !slices.Equal(got, tc.want) {
			t.Errorf("fieldDiff against %+v = %q, want %q", tc.b, got, tc.want)
		}
	}
	// An interface holding a slice on both sides compares as a type and
	// panics in Value.Equal; it is reported unnamed instead.
	if got := fieldDiff(cfg{held: []string{"y"}}, cfg{held: []string{"x"}}); !slices.Equal(got,
		[]string{"an unexported field"}) {
		t.Errorf("an interface field differing gives %q, want [an unexported field]", got)
	}
	if got := configDiff(config.Config{Profile: "a", ReadOnly: true}, config.Config{Profile: "a"}); !slices.Equal(got,
		[]string{"ReadOnly"}) {
		t.Errorf("configDiff = %q, want [ReadOnly]", got)
	}
}

// bundleSwitches is the switches the committed bundle asks for.
var bundleSwitches = []string{"enable_destructive", "read_only"}

// refusingPair is the committed manifest with GCM_LOCAL_DIR spending
// read_only and enable_destructive, under a folder that has a directory
// for every pair of their values but both on: so the server refuses the
// two together, and nothing else. Build refuses no pair today, so this
// is how the refusal check is made to fire.
func refusingPair(t *testing.T, edit func(map[string]any)) bundleManifest {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"false-false", "true-false", "false-true"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return editedManifest(t, func(d map[string]any) {
		dig(d, "server", "mcp_config", "env")["GCM_LOCAL_DIR"] =
			dir + string(filepath.Separator) + "${user_config.read_only}-${user_config.enable_destructive}"
		edit(d)
	})
}

// Every set of the switches is started, and the refused ones are held to
// the declared list both ways, each defect reported once. A declared key
// is a switch with a title, and its description names the others'.
func TestTheBundleRefusalsAreDeclared(t *testing.T) {
	committed := editedManifest(t, func(map[string]any) {})
	refusals := func(m bundleManifest, declared [][]string) []string {
		_, defaultsErr := defaultsProblems(m)
		return refusalProblems(m, bundleSwitches, declared, defaultsErr)
	}
	if got := refusals(refusingPair(t, func(map[string]any) {}), nil); len(got) != 1 || !strings.HasPrefix(got[0], undeclaredPair) {
		t.Errorf("an undeclared refusal gives %q, want the one problem %q", got, undeclaredPair)
	}
	pair := [][]string{{"enable_destructive", "read_only"}}
	// Declared, and each field naming the other's title, it passes; a
	// title is named without the space around it.
	named := func(d map[string]any) {
		dig(d, "user_config", "read_only")["description"] = "Refused with Deleting on."
		dig(d, "user_config", "enable_destructive")["description"] = "Refused with Read-only on."
		dig(d, "user_config", "enable_destructive")["title"] = " Deleting "
	}
	if got := refusals(refusingPair(t, named), pair); len(got) != 0 {
		t.Errorf("a declared refusal the fields name gives %q, want nothing", got)
	}
	got := refusals(refusingPair(t, func(d map[string]any) {
		dig(d, "user_config", "read_only")["description"] = "Register only the reads."
		dig(d, "user_config", "enable_destructive")["description"] = "Register the deletes."
	}), pair)
	want := []string{
		`user_config.enable_destructive does not name "Read-only", which the server refuses it with`,
		`user_config.read_only does not name "Deleting, and what cannot be taken back", which the server refuses it with`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("a declared refusal the fields do not name gives %q, want %q", got, want)
	}

	const starts = "bundleRefused declares that the server refuses enable_destructive, read_only together, and it starts with them"
	if got := refusals(committed, pair); !slices.Equal(got, []string{starts}) {
		t.Errorf("a declared refusal the server starts with gives %q, want %q once", got, starts)
	}
	// When the defaults do not start, no set is started: the defaults
	// check reports it.
	if got := refusalProblems(committed, bundleSwitches, pair, errors.New("the defaults do not start")); len(got) != 0 {
		t.Errorf("a set was started beside defaults that do not start: %q", got)
	}
	got = refusals(committed, [][]string{{"profile", "read_only"}})
	if !slices.Contains(got, "bundleRefused names profile, which is not a switch the bundle asks for") {
		t.Errorf("a declared key that is not a switch gives %q", got)
	}

	// read_only is in both sets, and a missing title is one problem, not
	// one per set, whether or not the defaults start.
	untitled := editedManifest(t, func(d map[string]any) { dig(d, "user_config", "read_only")["title"] = " " })
	twice := [][]string{{"enable_destructive", "read_only"}, {"read_only"}}
	for _, defaultsErr := range []error{nil, errors.New("the defaults do not start")} {
		got := refusalProblems(untitled, bundleSwitches, twice, defaultsErr)
		if n := len(slices.DeleteFunc(got, func(p string) bool {
			return !strings.HasPrefix(p, "user_config.read_only has no title")
		})); n != 1 {
			t.Errorf("defaults error %v: an untitled read_only is reported %d times, want 1", defaultsErr, n)
		}
	}
}

// undeclaredPair is the problem refusingPair gives with nothing declared.
const undeclaredPair = "the server refuses enable_destructive, read_only together, and bundleRefused does not declare it"

// A switch in a set is on, whatever its default: a required one with
// none, or one with a list, is not left out, which would hide a refusal.
func TestTheRefusalsTurnASwitchOnWhateverItsDefault(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"required with no default": func(d map[string]any) {
			dig(d, "user_config", "read_only")["required"] = true
			delete(dig(d, "user_config", "read_only"), "default")
		},
		"a list default": func(d map[string]any) { dig(d, "user_config", "read_only")["default"] = []any{"true"} },
	} {
		m := refusingPair(t, edit)
		_, defaultsErr := defaultsProblems(m)
		got := refusalProblems(m, bundleSwitches, nil, defaultsErr)
		if !slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, undeclaredPair) }) {
			t.Errorf("%s: got %q, want one starting %q", name, got, undeclaredPair)
		}
	}
}

// settingProblems runs the refusal check over the switches the bundle
// asks for, so a refusal Build adds fails the gate itself.
func TestSettingProblemsHoldTheRefusals(t *testing.T) {
	got := settingProblems(refusingPair(t, func(map[string]any) {}), bundleLeftOut)
	if !slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, undeclaredPair) }) {
		t.Errorf("settingProblems = %q, want one starting %q", got, undeclaredPair)
	}
	// Defaults the server refuses start no set, which would each fail
	// with them: the defaults problem is the one report.
	got = settingProblems(editedManifest(t, func(d map[string]any) {
		delete(dig(d, "user_config", "read_only"), "default")
	}), bundleLeftOut)
	if len(got) != 1 || !strings.HasPrefix(got[0], "the environment the bundle's defaults give the server is refused") {
		t.Errorf("defaults the server refuses give %q, want that one problem", got)
	}
}
