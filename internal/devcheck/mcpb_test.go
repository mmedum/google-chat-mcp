package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/mmedum/google-chat-mcp/internal/server"
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

// bundleManifest is the part of the manifest these gates read.
type bundleManifest struct {
	ManifestVersion string `json:"manifest_version"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Description     string `json:"description"`
	Server          struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command           string            `json:"command"`
			Args              []string          `json:"args"`
			Env               map[string]string `json:"env"`
			PlatformOverrides map[string]struct {
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			} `json:"platform_overrides"`
		} `json:"mcp_config"`
	} `json:"server"`
	UserConfig map[string]struct {
		Type     string `json:"type"`
		Required bool   `json:"required"`
		Default  any    `json:"default"`
	} `json:"user_config"`
	Compatibility struct {
		Platforms []string `json:"platforms"`
	} `json:"compatibility"`
}

// commands is every place the manifest names a file to run: the default
// and each platform override.
func (m bundleManifest) commands() []string {
	out := []string{m.Server.MCPConfig.Command}
	for _, o := range m.Server.MCPConfig.PlatformOverrides {
		if o.Command != "" {
			out = append(out, o.Command)
		}
	}
	return out
}

// userConfigRef matches a ${user_config.KEY} substitution.
var userConfigRef = regexp.MustCompile(`\$\{user_config\.([^}]+)\}`)

// references is every ${user_config.KEY} the manifest substitutes into a
// command, an argument or an environment variable.
func (m bundleManifest) references() []string {
	var refs []string
	collect := func(vals ...string) {
		for _, v := range vals {
			for _, match := range userConfigRef.FindAllStringSubmatch(v, -1) {
				refs = append(refs, match[1])
			}
		}
	}
	c := m.Server.MCPConfig
	collect(c.Command)
	collect(c.Args...)
	for _, v := range c.Env {
		collect(v)
	}
	for _, o := range c.PlatformOverrides {
		collect(o.Command)
		collect(o.Args...)
		for _, v := range o.Env {
			collect(v)
		}
	}
	slices.Sort(refs)
	return slices.Compact(refs)
}

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

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
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
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "packaging", "mcpb", "manifest.json"))
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

// fakeNPX puts an npx on PATH that records how it was called and keeps
// the directory it was asked to pack, so the test can look inside a
// bundle without installing Node.
func fakeNPX(t *testing.T, record string) string {
	t.Helper()
	bin := t.TempDir()
	const template = `#!/usr/bin/env bash
set -euo pipefail
stage=${@: -2:1}
out=${@: -1}
mkdir -p %[1]q/stage
printf '%%s\n' "$@" > %[1]q/args
cp -a "$stage/." %[1]q/stage/
: > "$out"
`
	script := fmt.Sprintf(template, record)
	path := filepath.Join(bin, "npx")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
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
		// 0644 on purpose: the pack script is what has to make it
		// executable, and mcpb only forces the mode on the entry point.
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

func runPack(t *testing.T, dist, record string) (string, error) {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "mcpb-pack.sh"), "v2.0.0", dist)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+fakeNPX(t, record)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestPackScriptStagesWhatTheManifestNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the pack script is bash, and it only ever runs on the release runner")
	}
	record := t.TempDir()
	dist := fakeDist(t, true, true, true)
	out, err := runPack(t, dist, record)
	if err != nil {
		t.Fatalf("pack failed: %v\n%s", err, out)
	}

	args, err := os.ReadFile(filepath.Join(record, "args"))
	if err != nil {
		t.Fatal(err)
	}
	// The pinned CLI, not whatever is current that morning.
	if !strings.Contains(string(args), "@anthropic-ai/mcpb@2.1.2") {
		t.Errorf("npx called without the pinned mcpb version:\n%s", args)
	}
	if !strings.Contains(string(args), "pack") {
		t.Errorf("npx called without pack:\n%s", args)
	}

	stage := filepath.Join(record, "stage")
	staged, err := os.ReadFile(filepath.Join(stage, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m bundleManifest
	if err := json.Unmarshal(staged, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != "2.0.0" {
		t.Errorf("staged manifest version %q, want 2.0.0 from the tag", m.Version)
	}

	// The point of the whole test: whatever the manifest says it runs
	// has to be in the bundle, and executable.
	for _, command := range m.commands() {
		rel := strings.TrimPrefix(command, "${__dirname}/")
		if rel == command {
			t.Errorf("command %q is not under ${__dirname}", command)
			continue
		}
		info, err := os.Stat(filepath.Join(stage, rel))
		if err != nil {
			t.Errorf("manifest runs %s, which the bundle does not carry: %v", rel, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable in the bundle (%v)", rel, info.Mode().Perm())
		}
	}
	if m.Server.EntryPoint == "" {
		t.Fatal("the staged manifest declares no entry point")
	}
	if _, err := os.Stat(filepath.Join(stage, m.Server.EntryPoint)); err != nil {
		t.Errorf("entry point %s is missing from the bundle: %v", m.Server.EntryPoint, err)
	}
	// Linux is served by a launcher and two binaries, because a manifest
	// has no key for the architecture and Claude Desktop for Linux ships
	// both. All three have to be in the bundle or the override names a
	// file that is not there.
	for _, name := range []string{
		"server/linux-launch.sh", "server/google-chat-mcp-amd64", "server/google-chat-mcp-arm64",
	} {
		info, err := os.Stat(filepath.Join(stage, name))
		if err != nil {
			t.Errorf("%s is missing from the bundle: %v", name, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable in the bundle (%v); mcpb forces the mode on the "+
				"entry point alone", name, info.Mode().Perm())
		}
	}
	for _, name := range []string{"LICENSE", "README.md"} {
		if _, err := os.Stat(filepath.Join(stage, name)); err != nil {
			t.Errorf("%s is missing from the bundle: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dist, "google-chat-mcp_2.0.0.mcpb")); err != nil {
		t.Errorf("the bundle was not written where checksums.txt will look for it: %v", err)
	}
}

func TestPackScriptRefusesAnIncompleteDist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the pack script is bash, and it only ever runs on the release runner")
	}
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
			out, err := runPack(t, fakeDist(t, tt.darwin, tt.windows, tt.linux), t.TempDir())
			if tt.want {
				if err != nil {
					t.Fatalf("pack failed: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("packed a dist with a binary missing:\n%s", out)
			}
			if !strings.Contains(out, "expected exactly one") {
				t.Errorf("unhelpful failure:\n%s", out)
			}
		})
	}
}
