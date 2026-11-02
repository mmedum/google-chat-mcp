package main

import (
	"cmp"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/josephspurrier/goversioninfo"

	"github.com/mmedum/google-chat-mcp/v9/internal/server"
)

// The Windows version resource, which code signing needs and Go does not
// write. goreleaser's before hook runs this to write one .syso per
// Windows architecture, where the linker takes it; docs/architecture.md,
// Distribution, says why it is built this way and kept out of git.

// winresDir is the package goreleaser builds, which is where the linker
// looks for the .syso files.
const winresDir = "cmd/" + server.Name

// winresArches are the Windows architectures the release builds.
var winresArches = []string{"amd64", "arm64"}

func winres(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "gates: winres: %v\n", err)
		return 1
	}
	vi, err := winresInfo(strings.TrimPrefix(args[1], "v"))
	if err != nil {
		return fail(err)
	}
	dir := cmp.Or(argAt(args, 2), winresDir)
	for _, arch := range winresArches {
		path := filepath.Join(dir, "rsrc_windows_"+arch+".syso")
		if err := vi.WriteSyso(path, arch); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintln(stdout, path)
	}
	return 0
}

// winresInfo is the version resource for one release. Every version
// attribute holds the same version, as SignPath Foundation's terms
// require: the file and product versions, in the fixed part and in the
// strings. The strings keep the version whole, a pre-release or build
// included; the fixed part keeps its three numbers and a 0.
func winresInfo(version string) (*goversioninfo.VersionInfo, error) {
	if problem := versionProblem(version); problem != "" {
		return nil, fmt.Errorf("version %q: %s", version, problem)
	}
	m := releaseVersion.FindStringSubmatch(version)
	var n [3]int
	for i := range n {
		v, err := strconv.ParseUint(m[i+1], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("version %q: %s does not fit the 16 bits Windows keeps for each number",
				version, m[i+1])
		}
		n[i] = int(v)
	}
	fixed := goversioninfo.FileVersion{Major: n[0], Minor: n[1], Patch: n[2]}

	vi := &goversioninfo.VersionInfo{}
	vi.FixedFileInfo = goversioninfo.FixedFileInfo{
		FileVersion:    fixed,
		ProductVersion: fixed,
		// All flags defined, none set: a release build. FileOS is
		// VOS_NT_WINDOWS32 and FileType VFT_APP, both from Microsoft's
		// VS_FIXEDFILEINFO reference.
		FileFlagsMask: "3f",
		FileOS:        "040004",
		FileType:      "01",
	}
	vi.StringFileInfo = goversioninfo.StringFileInfo{
		// Short, not server.Description: Windows shows it as the
		// program's name, in Task Manager among other places.
		FileDescription:  "MCP server for Google Chat",
		FileVersion:      version,
		InternalName:     server.Name,
		OriginalFilename: server.Name + ".exe",
		ProductName:      server.Name,
		ProductVersion:   version,
	}
	// The strings are U.S. English in Unicode, and the translation table
	// says so, which is how a reader finds them.
	vi.VarFileInfo = goversioninfo.VarFileInfo{Translation: goversioninfo.Translation{
		LangID:    goversioninfo.LngUSEnglish,
		CharsetID: goversioninfo.CsUnicode,
	}}
	vi.Build()
	vi.Walk()
	return vi, nil
}
