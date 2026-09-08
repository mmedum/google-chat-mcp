package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/mmedum/google-chat-mcp/v2/internal/server"
)

// serverJSON writes the MCP registry entry for a release.
//
// The registry demands a SHA-256 for an MCPB package and clients check
// the download against it, which is why this is not goreleaser's `mcp`
// block: that block takes a registry type, an identifier and a
// transport, and has nowhere to put a hash. The hash comes from the
// release's own checksums.txt, so the entry describes the bytes that
// were published rather than a rebuild of them.
//
// Everything else is derived: the repository and the registry namespace
// from the module path, the description from the server, the file name
// and version from the checksum file and the tag.

// registrySchema is the server.json format this entry is written to.
const registrySchema = "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json"

// descriptionMax is the registry's cap on a description.
const descriptionMax = 100

// sha256Line matches a checksums.txt row: the hash, then the file name.
var sha256Line = regexp.MustCompile(`^([a-f0-9]{64})\s+\*?(\S+)$`)

type registryEntry struct {
	Schema      string            `json:"$schema"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	WebsiteURL  string            `json:"websiteUrl"`
	Repository  registryRepo      `json:"repository"`
	Packages    []registryPackage `json:"packages"`
}

type registryRepo struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type registryPackage struct {
	RegistryType string            `json:"registryType"`
	Identifier   string            `json:"identifier"`
	FileSHA256   string            `json:"fileSha256"`
	Version      string            `json:"version"`
	Transport    registryTransport `json:"transport"`
}

type registryTransport struct {
	Type string `json:"type"`
}

// githubRepo splits a module path into the owner and repository the
// registry namespace and the download URL are built from. Both have to
// be the account that publishes, and the module path is the one place
// that is already true — a constant beside it would be a second copy
// with nothing keeping it honest.
func githubRepo(module string) (owner, name string, err error) {
	// A module at v2 or above carries its major version as a final path
	// element — Go requires it, and it is not part of the repository
	// name. Stripped rather than rejected: this refused
	// github.com/mmedum/google-chat-mcp/v2 outright, which would have
	// failed the release at the tag, in public, the first time the
	// module went to v2.
	parts := strings.Split(module, "/")
	if len(parts) == 4 && majorVersion.MatchString(parts[3]) {
		parts = parts[:3]
	}
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("module path %q is not github.com/OWNER/REPO", module)
	}
	return parts[1], parts[2], nil
}

// majorVersion matches the /vN a module path carries from v2 onwards.
var majorVersion = regexp.MustCompile(`^v[1-9][0-9]*$`)

func moduleRepo() (owner, name string, err error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", fmt.Errorf("no build info")
	}
	return githubRepo(info.Main.Path)
}

// bundle finds the single .mcpb row in a checksums file. Two bundles or
// none is a release that did not build the way it was meant to, and
// picking one of them would publish a hash for a file nobody chose.
func bundle(checksums string) (name, sum string, err error) {
	data, err := os.ReadFile(checksums) //nolint:gosec // a path the release passes in
	if err != nil {
		return "", "", err
	}
	var rows int
	for _, line := range strings.Split(string(data), "\n") {
		m := sha256Line.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		rows++
		if path.Ext(m[2]) != ".mcpb" {
			continue
		}
		if name != "" {
			return "", "", fmt.Errorf("%s lists more than one .mcpb: %s and %s", checksums, name, m[2])
		}
		name, sum = path.Base(m[2]), m[1]
	}
	if rows == 0 {
		return "", "", fmt.Errorf("%s has no checksum rows", checksums)
	}
	if name == "" {
		return "", "", fmt.Errorf("%s lists no .mcpb among %d rows", checksums, rows)
	}
	return name, sum, nil
}

func serverJSON(version, checksums string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintln(stderr, "server-json:", err)
		return 1
	}
	owner, repo, err := moduleRepo()
	if err != nil {
		return fail(err)
	}
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	semver := strings.TrimPrefix(tag, "v")
	if semver == "" {
		return fail(fmt.Errorf("empty version"))
	}
	if len(server.Description) > descriptionMax {
		return fail(fmt.Errorf("description is %d characters, the registry allows %d",
			len(server.Description), descriptionMax))
	}
	name, sum, err := bundle(checksums)
	if err != nil {
		return fail(err)
	}
	// The registry refuses a package URL that is not a release asset on
	// github.com or gitlab.com, and one that does not carry "mcp"
	// somewhere. Both hold by construction here.
	entry := registryEntry{
		Schema:      registrySchema,
		Name:        fmt.Sprintf("io.github.%s/%s", owner, repo),
		Description: server.Description,
		Version:     semver,
		WebsiteURL:  fmt.Sprintf("https://github.com/%s/%s#readme", owner, repo),
		Repository: registryRepo{
			URL:    fmt.Sprintf("https://github.com/%s/%s", owner, repo),
			Source: "github",
		},
		Packages: []registryPackage{{
			RegistryType: "mcpb",
			Identifier: fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
				owner, repo, tag, name),
			FileSHA256: sum,
			Version:    semver,
			Transport:  registryTransport{Type: "stdio"},
		}},
	}
	out, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fail(err)
	}
	_, _ = fmt.Fprintln(stdout, string(out))
	return 0
}

// placeholderVersion is the version the committed bundle manifest
// carries. A release replaces it; a bundle still carrying it was built
// without this step.
const placeholderVersion = "0.0.0-dev"
