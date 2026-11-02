package main

import (
	"cmp"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/josephspurrier/goversioninfo"
)

// The Windows version resource.
//
// Windows reads a program's name and version from a resource inside the
// .exe, and Go writes none. Code signing needs one: SignPath Foundation
// signs a Windows program only when its resource names the project and
// carries the version the release asked to sign. The linker takes a
// resource from a .syso file in the package it builds, one per
// architecture, so goreleaser's before hook runs this to write them.
//
// They are gitignored rather than committed. A committed one would carry
// one release's version into every build after it. An untracked one that
// is not ignored marks the tree modified, which Go stamps into the binary
// as vcs.modified=true.

// winresProduct names the program in the resource. SignPath
// Foundation's terms require every product name attribute to be the
// project's name.
const winresProduct = "google-chat-mcp"

// winresDir is the package goreleaser builds, which is where the linker
// looks for the .syso files.
const winresDir = "cmd/google-chat-mcp"

// winresArches are the Windows architectures the release builds.
var winresArches = []string{"amd64", "arm64"}

// winresVersion is a version the resource can hold. The fixed part keeps
// four 16-bit numbers. The strings keep the version as given, so a
// suffix such as -rc1, or a rehearsal's -SNAPSHOT-<commit>, survives
// there and nowhere else.
var winresVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

func winres(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "gates: winres: %v\n", err)
		return 1
	}
	vi, err := winresInfo(args[1])
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
// attribute holds the same version, which SignPath Foundation's terms
// require: the file and product versions, in the fixed part and in the
// strings.
func winresInfo(version string) (*goversioninfo.VersionInfo, error) {
	m := winresVersion.FindStringSubmatch(version)
	if m == nil {
		return nil, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH with an optional -suffix: "+
			"pass goreleaser's {{ .Version }}, not {{ .Tag }}", version)
	}
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
		FileDescription:  "MCP server for Google Chat",
		FileVersion:      version,
		InternalName:     winresProduct,
		OriginalFilename: winresProduct + ".exe",
		ProductName:      winresProduct,
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
