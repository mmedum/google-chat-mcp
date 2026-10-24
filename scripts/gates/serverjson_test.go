package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/v7/internal/server"
)

// testdata/server-2025-12-11.schema.json is the registry's published
// server.json schema, fetched 2026-09-06 from
// static.modelcontextprotocol.io. The rules the registry enforces in
// code rather than in the schema — an MCPB package needs a hash, the
// URL is a release asset on github.com and carries "mcp", and
// registryBaseUrl must be absent — are checked here beside it.

const (
	archiveSum = "1111111111111111111111111111111111111111111111111111111111111111"
	bundleSum  = "2222222222222222222222222222222222222222222222222222222222222222"
)

func checksums(t *testing.T, rows ...string) string {
	t.Helper()
	return write(t, "checksums.txt", strings.Join(rows, "\n")+"\n")
}

func TestServerJSONMatchesTheRegistrySchema(t *testing.T) {
	path := checksums(t,
		archiveSum+"  google-chat-mcp_2.0.0_linux_amd64.tar.gz",
		bundleSum+"  google-chat-mcp_2.0.0.mcpb",
	)
	var out, errOut bytes.Buffer
	if code := run([]string{"server-json", "v2.0.0", path}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}

	var entry map[string]any
	if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(entry) < 6 {
		t.Fatalf("entry has %d fields, expected the full one: %s", len(entry), out.String())
	}
	resolved, schema := resolveSchema(t, "server-2025-12-11.schema.json")
	if len(schema.Definitions) < 10 {
		t.Fatalf("vendored schema has %d definitions, expected the published one", len(schema.Definitions))
	}
	if err := resolved.Validate(entry); err != nil {
		t.Fatalf("does not match the registry schema: %v\n%s", err, out.String())
	}

	packages, _ := entry["packages"].([]any)
	if len(packages) != 1 {
		t.Fatalf("want one package, got %d", len(packages))
	}
	pkg, _ := packages[0].(map[string]any)

	// The registry validates an MCPB package in code: a hash it demands,
	// a GitHub or GitLab release asset, "mcp" in the URL, and no
	// registryBaseUrl. A rejection here would only show on release day.
	if pkg["fileSha256"] != bundleSum {
		t.Errorf("fileSha256 is %v, want the bundle's row from checksums.txt", pkg["fileSha256"])
	}
	if _, ok := pkg["registryBaseUrl"]; ok {
		t.Error("registryBaseUrl is set; the registry refuses it on an MCPB package")
	}
	identifier, _ := pkg["identifier"].(string)
	const want = "https://github.com/mmedum/google-chat-mcp/releases/download/v2.0.0/google-chat-mcp_2.0.0.mcpb"
	if identifier != want {
		t.Errorf("identifier\n got %q\nwant %q", identifier, want)
	}
	release := regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+/releases/download/[^/]+/[^/]+$`)
	if !release.MatchString(identifier) {
		t.Errorf("identifier is not a release asset URL: %s", identifier)
	}
	if !strings.Contains(strings.ToLower(identifier), "mcp") {
		t.Errorf("identifier does not carry \"mcp\": %s", identifier)
	}
	if entry["description"] != server.Description {
		t.Errorf("description %v, want the server's own", entry["description"])
	}
	if entry["version"] != "2.0.0" {
		t.Errorf("version %v, want the tag without its v", entry["version"])
	}
}

func TestServerJSONTakesTheTagWithOrWithoutItsV(t *testing.T) {
	path := checksums(t, bundleSum+"  google-chat-mcp_2.0.0.mcpb")
	for _, version := range []string{"v2.0.0", "2.0.0"} {
		var out, errOut bytes.Buffer
		if code := run([]string{"server-json", version, path}, &out, &errOut); code != 0 {
			t.Fatalf("%s: exit %d: %s", version, code, errOut.String())
		}
		if !strings.Contains(out.String(), "/releases/download/v2.0.0/") {
			t.Errorf("%s: the download URL does not use the tag: %s", version, out.String())
		}
		if !strings.Contains(out.String(), `"version": "2.0.0"`) {
			t.Errorf("%s: the version is not the semantic one: %s", version, out.String())
		}
	}
}

func TestServerJSONRefusesAnUncertainRelease(t *testing.T) {
	tests := []struct {
		name string
		rows []string
		want string
	}{
		{
			name: "no bundle",
			rows: []string{archiveSum + "  google-chat-mcp_2.0.0_linux_amd64.tar.gz"},
			want: "lists no .mcpb",
		},
		{
			name: "two bundles",
			rows: []string{
				bundleSum + "  google-chat-mcp_2.0.0.mcpb",
				archiveSum + "  google-chat-mcp_2.0.0_beta.mcpb",
			},
			want: "more than one .mcpb",
		},
		{
			name: "nothing to read",
			rows: []string{"", "not a checksum line"},
			want: "no checksum rows",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run([]string{"server-json", "v2.0.0", checksums(t, tt.rows...)}, &out, &errOut); code == 0 {
				t.Fatalf("wrote an entry anyway:\n%s", out.String())
			}
			if !strings.Contains(errOut.String(), tt.want) {
				t.Errorf("error %q does not say %q", errOut.String(), tt.want)
			}
		})
	}
}

func TestServerJSONReportsAMissingChecksumFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"server-json", "v2.0.0", "no/such/checksums.txt"}, &out, &errOut); code == 0 {
		t.Fatalf("wrote an entry without a checksum file:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "no/such/checksums.txt") {
		t.Errorf("error does not name the file: %s", errOut.String())
	}
}

func TestDescriptionFitsTheRegistry(t *testing.T) {
	if n := len(server.Description); n > descriptionMax {
		t.Errorf("server.Description is %d characters; the registry allows %d", n, descriptionMax)
	}
}

func TestGitHubRepoRefusesAnythingElse(t *testing.T) {
	tests := []struct {
		module      string
		owner, name string
		ok          bool
	}{
		{module: "github.com/mmedum/google-chat-mcp", owner: "mmedum", name: "google-chat-mcp", ok: true},
		// A module at v2 or above: the major version is Go's, not the
		// repository's, so it comes off.
		{module: "github.com/mmedum/google-chat-mcp/v7", owner: "mmedum", name: "google-chat-mcp", ok: true},
		{module: "github.com/mmedum/google-chat-mcp/v10", owner: "mmedum", name: "google-chat-mcp", ok: true},
		{module: "gitlab.com/mmedum/google-chat-mcp"},
		{module: "github.com/mmedum/google-chat-mcp/v7/v2"},
		{module: "github.com/mmedum/google-chat-mcp/internal"},
		{module: "github.com/mmedum/google-chat-mcp/v0"},
		{module: "github.com/mmedum"},
		{module: "github.com//google-chat-mcp"},
		{module: ""},
	}
	for _, tt := range tests {
		t.Run(tt.module, func(t *testing.T) {
			owner, name, err := githubRepo(tt.module)
			if tt.ok != (err == nil) {
				t.Fatalf("githubRepo(%q) error = %v, want ok = %v", tt.module, err, tt.ok)
			}
			if owner != tt.owner || name != tt.name {
				t.Errorf("githubRepo(%q) = %q, %q; want %q, %q", tt.module, owner, name, tt.owner, tt.name)
			}
		})
	}
}

// runManifest runs mcpb-manifest on raw from the repository root, where
// the checks read the schema, the launcher and go.mod.
func runManifest(t *testing.T, version string, raw []byte) (stdout, stderr string, code int) {
	t.Helper()
	path := write(t, "manifest.json", string(raw))
	t.Chdir(repoRoot(t))
	var out, errOut bytes.Buffer
	code = run([]string{"mcpb-manifest", version, path}, &out, &errOut)
	return out.String(), errOut.String(), code
}

// The committed manifest comes out byte for byte, its version stamped.
func TestMCPBManifestStampsTheVersion(t *testing.T) {
	committed := readRepoFile(t, manifestSource)
	out, errOut, code := runManifest(t, "v2.0.0", []byte(committed))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var stamped map[string]any
	if err := json.Unmarshal([]byte(out), &stamped); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if stamped["version"] != "2.0.0" {
		t.Errorf("version %v, want the tag without its v", stamped["version"])
	}
	if want := strings.Replace(committed, `"0.0.0-dev"`, `"2.0.0"`, 1); out != want {
		t.Errorf("the rest of the manifest was rewritten:\n%s", out)
	}
}

// mcpb-manifest runs every check the packer runs, so it prints no
// manifest the packer would refuse.
func TestMCPBManifestRefusesWhatItCannotStamp(t *testing.T) {
	tests := []struct {
		name, version string
		raw           []byte
		want          string
	}{
		{
			name:    "already stamped",
			version: "2.0.0",
			raw:     editedRaw(t, func(d map[string]any) { d["version"] = "1.9.0" }),
			want:    `the manifest carries version "1.9.0"; the committed one carries the placeholder "0.0.0-dev"`,
		},
		{
			name:    "no version at all",
			version: "2.0.0",
			raw:     editedRaw(t, func(d map[string]any) { delete(d, "version") }),
			want:    `the manifest carries version ""; the committed one carries the placeholder "0.0.0-dev"`,
		},
		{
			name:    "a manifest the packer refuses",
			version: "2.0.0",
			raw:     editedRaw(t, func(d map[string]any) { d["name"] = "google-chat" }),
			want:    `manifest name "google-chat", server name "google-chat-mcp"`,
		},
		{
			name:    "not json",
			version: "2.0.0",
			raw:     []byte("manifest_version: 0.3\n"),
			want:    "manifest.json: invalid character",
		},
		{
			name:    "no version given",
			version: "v",
			raw:     []byte(readRepoFile(t, manifestSource)),
			want:    "empty version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := runManifest(t, tt.version, tt.raw)
			if code == 0 {
				t.Fatalf("stamped it anyway:\n%s", out)
			}
			if !strings.Contains(errOut, tt.want) {
				t.Errorf("error %q does not say %q", errOut, tt.want)
			}
		})
	}
}

// The substitution replaces the version and nothing else, or refuses.
func TestRenderManifestReplacesTheVersionAlone(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"the placeholder twice", `{"version": "0.0.0-dev", "x": "0.0.0-dev"}`,
			`the manifest spells "0.0.0-dev" 2 times; the substitution needs exactly one`},
		{"the placeholder somewhere else", `{"x": "0.0.0-dev", "version": "1.9.0"}`,
			`the substitution set version to "1.9.0", wanted "2.0.0"`},
	} {
		if _, err := renderManifest("2.0.0", []byte(tc.raw)); err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}
