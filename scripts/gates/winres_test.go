package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/mmedum/google-chat-mcp/v9/internal/server"
)

// versionResource is what a reader of a Windows program's version
// resource sees: the fixed version numbers, the string table's
// language and code page, and its strings.
type versionResource struct {
	fileVersion    [4]uint16
	productVersion [4]uint16
	table          string
	strings        map[string]string
}

// The resource is what SignPath's metadata restrictions read and what
// Windows shows under the file's properties, so it is checked in a
// program the Go linker built, on both architectures the release ships.
// A .syso the linker drops, or links for the wrong architecture, fails
// here rather than at a signing request.
func TestWinresLinksIntoAWindowsProgram(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "go.mod", "module example.com/winrestest\n\ngo 1.24\n")
	put(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	// The tag, as a hook passing {{ .Tag }} would: the v is dropped, as
	// every other release gate drops it.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"winres", "v9.1.2", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("winres v9.1.2 exited %d: %s", code, stderr.String())
	}

	want := versionResource{
		fileVersion:    [4]uint16{9, 1, 2, 0},
		productVersion: [4]uint16{9, 1, 2, 0},
		table:          "040904B0",
		strings: map[string]string{
			"FileDescription":  "MCP server for Google Chat",
			"FileVersion":      "9.1.2",
			"InternalName":     "google-chat-mcp",
			"OriginalFilename": "google-chat-mcp.exe",
			"ProductName":      "google-chat-mcp",
			"ProductVersion":   "9.1.2",
		},
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "program.exe")
			build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", exe, ".")
			build.Dir = dir
			build.Env = append(os.Environ(), "GOOS=windows", "GOARCH="+arch, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("go build for windows/%s: %v\n%s", arch, err, out)
			}
			got := readVersionResource(t, peVersionResource(t, exe))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("windows/%s program carries\n%#v\nwant\n%#v", arch, got, want)
			}
		})
	}
}

// Every version attribute holds the version the release was cut at,
// which SignPath Foundation's terms require. The fixed part keeps the
// numbers; the strings keep the whole version, a suffix included.
func TestWinresVersionAttributesAgree(t *testing.T) {
	for _, tc := range []struct {
		version string
		fixed   [4]uint16
	}{
		{"9.1.2", [4]uint16{9, 1, 2, 0}},
		{"0.0.1", [4]uint16{0, 0, 1, 0}},
		{"10.0.0-rc1", [4]uint16{10, 0, 0, 0}},
		{"9.1.2-SNAPSHOT-90570dc", [4]uint16{9, 1, 2, 0}},
		{"1.2.3+build.5", [4]uint16{1, 2, 3, 0}},
		{"65535.65535.65535", [4]uint16{65535, 65535, 65535, 0}},
	} {
		vi, err := winresInfo(tc.version)
		if err != nil {
			t.Errorf("winresInfo(%q): %v", tc.version, err)
			continue
		}
		got := readVersionResource(t, vi.Buffer.Bytes())
		if got.fileVersion != tc.fixed || got.productVersion != tc.fixed {
			t.Errorf("winresInfo(%q) fixed versions are file %v and product %v, want %v for both",
				tc.version, got.fileVersion, got.productVersion, tc.fixed)
		}
		if got.strings["FileVersion"] != tc.version || got.strings["ProductVersion"] != tc.version {
			t.Errorf("winresInfo(%q) strings are FileVersion %q and ProductVersion %q, want %q for both",
				tc.version, got.strings["FileVersion"], got.strings["ProductVersion"], tc.version)
		}
	}
}

// A version the resource cannot hold is refused before anything is
// written, so a release never builds with a stale or partial resource.
func TestWinresRefusesAVersionItCannotHold(t *testing.T) {
	for _, version := range []string{
		"",
		"9.1",       // no patch number
		"9.1.2.3",   // four numbers: goreleaser never gives one
		"09.1.2",    // a leading zero, which Semantic Versioning refuses
		"9.1.2 ",    // a stray space from a hook
		"65536.0.0", // one past what Windows keeps for a number
		"9.1.65536",
	} {
		dir := t.TempDir()
		var stdout, stderr bytes.Buffer
		if code := run([]string{"winres", version, dir}, &stdout, &stderr); code != 1 {
			t.Errorf("winres %q exited %d, want 1", version, code)
		}
		if !strings.HasPrefix(stderr.String(), "gates: winres: ") {
			t.Errorf("winres %q said %q on stderr, want the reason", version, stderr.String())
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("winres %q wrote %d files, want none", version, len(entries))
		}
	}
}

// Rebuilding a tag gives byte-identical binaries, which mod_timestamp
// and -trimpath are there for. The resource has to come out the same
// on every run too, or it would be the one input that differs.
func TestWinresIsByteIdenticalAcrossRuns(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, dir := range []string{first, second} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"winres", "9.1.2", dir}, &stdout, &stderr); code != 0 {
			t.Fatalf("winres 9.1.2 exited %d: %s", code, stderr.String())
		}
	}
	for _, name := range []string{"rsrc_windows_amd64.syso", "rsrc_windows_arm64.syso"} {
		a, errA := os.ReadFile(filepath.Join(first, name))
		b, errB := os.ReadFile(filepath.Join(second, name))
		if errA != nil || errB != nil {
			t.Fatalf("reading %s: %v, %v", name, errA, errB)
		}
		if len(a) < 512 {
			t.Errorf("%s is %d bytes, which is too small to hold the resource", name, len(a))
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs between two runs at the same version", name)
		}
	}
}

// The resource reaches a program only from the package goreleaser
// builds, for the architectures it builds, and only if git ignores it:
// an untracked file that is not ignored stamps the binary modified.
func TestWinresWritesWhereGoreleaserBuilds(t *testing.T) {
	config := uncommented(readRepoFile(t, ".goreleaser.yaml"))
	for _, want := range []string{
		"main: ./" + winresDir + "\n",
		"binary: " + server.Name + "\n",
		"goarch: [" + strings.Join(winresArches, ", ") + "]\n",
	} {
		if !strings.Contains(config, want) {
			t.Errorf(".goreleaser.yaml has no %q", strings.TrimSpace(want))
		}
	}
	for _, arch := range winresArches {
		path := winresDir + "/rsrc_windows_" + arch + ".syso"
		check := exec.Command("git", "check-ignore", "-q", path)
		check.Dir = repoRoot(t)
		if err := check.Run(); err != nil {
			t.Errorf("git does not ignore %s (%v), so the release would build from a modified tree", path, err)
		}
	}
}

// peVersionResource is the version resource in a Windows program: the
// RT_VERSION entry of its .rsrc section, first name, first language.
func peVersionResource(t *testing.T, exe string) []byte {
	t.Helper()
	f, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sec := f.Section(".rsrc")
	if sec == nil {
		t.Fatalf("%s has no .rsrc section: the linker took no resource", exe)
	}
	data, err := sec.Data()
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	// entry is the target of one entry in the resource directory at
	// offset dir: the entry named id, or the first when id is negative.
	entry := func(dir uint32, id int64) uint32 {
		count := uint32(le.Uint16(data[dir+12:])) + uint32(le.Uint16(data[dir+14:]))
		for i := range count {
			e := dir + 16 + 8*i
			if id < 0 || int64(le.Uint32(data[e:])) == id {
				return le.Uint32(data[e+4:])
			}
		}
		t.Fatalf("%s: no resource entry %d in the directory at %#x", exe, id, dir)
		return 0
	}
	const subdir = 1 << 31
	const rtVersion = 16
	name := entry(0, rtVersion)
	lang := entry(name&^subdir, -1)
	leaf := entry(lang&^subdir, -1)
	if name&subdir == 0 || lang&subdir == 0 || leaf&subdir != 0 {
		t.Fatalf("%s: the version resource is not type, name, language deep", exe)
	}
	rva, size := le.Uint32(data[leaf:]), le.Uint32(data[leaf+4:])
	start := rva - sec.VirtualAddress
	return data[start : start+size]
}

// readVersionResource parses a VS_VERSIONINFO block as Microsoft's
// reference lays it out.
func readVersionResource(t *testing.T, b []byte) versionResource {
	t.Helper()
	root, _ := parseVersionNode(t, b, 0)
	if root.key != "VS_VERSION_INFO" || len(root.value) != 52 {
		t.Fatalf("the block is %q with a %d-byte value, want VS_VERSION_INFO with 52", root.key, len(root.value))
	}
	le := binary.LittleEndian
	if sig := le.Uint32(root.value); sig != 0xFEEF04BD {
		t.Fatalf("VS_FIXEDFILEINFO signature is %#x, want 0xfeef04bd", sig)
	}
	split := func(at int) [4]uint16 {
		ms, ls := le.Uint32(root.value[at:]), le.Uint32(root.value[at+4:])
		return [4]uint16{uint16(ms >> 16), uint16(ms), uint16(ls >> 16), uint16(ls)}
	}
	got := versionResource{fileVersion: split(8), productVersion: split(16), strings: map[string]string{}}
	for _, child := range root.children {
		if child.key != "StringFileInfo" || len(child.children) != 1 {
			continue
		}
		table := child.children[0]
		got.table = table.key
		for _, s := range table.children {
			got.strings[s.key] = utf16String(s.value)
		}
	}
	return got
}

// versionNode is one node of a VS_VERSIONINFO block.
type versionNode struct {
	key      string
	value    []byte
	children []versionNode
}

// utf16String decodes UTF-16LE up to its first NUL.
func utf16String(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// parseVersionNode reads the node at start: its length, value length
// and type, a NUL-terminated UTF-16 key, padding to 32 bits, the value,
// padding again, and its children up to the length. A text value's
// length counts 16-bit words, a binary one's bytes.
func parseVersionNode(t *testing.T, b []byte, start int) (versionNode, int) {
	t.Helper()
	le := binary.LittleEndian
	align := func(p int) int { return (p + 3) &^ 3 }
	if start+6 > len(b) {
		t.Fatalf("version node at %d runs past the %d-byte block", start, len(b))
	}
	end := start + int(le.Uint16(b[start:]))
	valueLen := int(le.Uint16(b[start+2:]))
	if le.Uint16(b[start+4:]) == 1 {
		valueLen *= 2
	}
	if end > len(b) || end < start+6 {
		t.Fatalf("version node at %d claims to end at %d, outside the %d-byte block", start, end, len(b))
	}
	var n versionNode
	n.key = utf16String(b[start+6 : end])
	p := align(start + 6 + 2*(len(utf16.Encode([]rune(n.key)))+1))
	if p+valueLen > end {
		t.Fatalf("version node %q has a %d-byte value that runs past its end", n.key, valueLen)
	}
	n.value = b[p : p+valueLen]
	for p = align(p + valueLen); p < end; {
		child, next := parseVersionNode(t, b, p)
		n.children = append(n.children, child)
		p = align(next)
	}
	return n, end
}
