package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The Claude Desktop bundle.
//
// A .mcpb is a deflate zip with manifest.json at the root and the
// binaries under server/, which archive/zip writes in about forty lines.
// It used to shell out to `npx @anthropic-ai/mcpb pack`, and what that
// bought was schema validation.
//
// That is not the validation worth having. A schema says the manifest is
// well formed. It cannot say that entry_point names a file in the
// bundle, that a platform override's command points at something
// staged, or that a ${user_config.x} refers to a key user_config
// declares. All three produce a bundle that packs, installs cleanly, and
// then does nothing. Those three are checked here against the tree about
// to be packed; the schema is checked by a test, against a vendored copy.

// manifestSource is the manifest as the repository carries it, with the
// placeholder version still in it.
var manifestSource = filepath.Join("packaging", "mcpb", "manifest.json")

// launcherSource is the Linux launcher.
var launcherSource = filepath.Join("packaging", "mcpb", "linux-launch.sh")

// manifestName is where the manifest sits inside the bundle. The
// installer looks at the root and nowhere else.
const manifestName = "manifest.json"

// dirnameRef prefixes a manifest command that names a file inside the
// installed bundle.
const dirnameRef = "${__dirname}/"

// serverDir holds everything the bundle runs.
const serverDir = "server/"

// zipEpoch is the modification time every entry carries.
//
// Set at all, because an unset time writes zeroes that display as the
// impossible 1980-00-00. Fixed, because the same inputs then make the
// same archive byte for byte, which is the least to offer someone
// checking a checksum. This is the earliest a zip can express, so it
// reads as deliberately not a build time rather than as a wrong one.
var zipEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// bundleFile is one entry: what it is called inside the bundle, and
// where it comes from.
type bundleFile struct {
	name string
	from string
}

// mcpbPack writes the Claude Desktop bundle from the binaries goreleaser
// just built.
//
// It runs as the universal binary's post hook, which is the one point in
// the pipeline where every binary exists and the checksum file has not
// been written yet. That is what puts the bundle in checksums.txt with
// the archives, under the same signature.
func mcpbPack(args []string, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "gates: mcpb-pack: "+format+"\n", a...)
		return 1
	}
	// Trimmed here for the bundle's filename; renderManifest is what
	// refuses an empty one, so the rule lives in one place.
	version := strings.TrimPrefix(args[1], "v")
	dist := cmp.Or(argAt(args, 2), "dist")

	files, err := bundleLayout(dist)
	if err != nil {
		return fail("%v", err)
	}
	manifest, err := renderManifest(version, manifestSource)
	if err != nil {
		return fail("%v", err)
	}
	var m bundleManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return fail("the rendered manifest: %v", err)
	}
	if problems := checkManifest(m, files); len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "gates: mcpb-pack: "+p)
		}
		return 1
	}

	out := filepath.Join(dist, fmt.Sprintf("google-chat-mcp_%s.mcpb", version))
	if err := writeBundle(out, files, manifest); err != nil {
		return fail("%v", err)
	}
	_, _ = fmt.Fprintf(stdout, "mcpb-pack: wrote %s\n", out)
	return 0
}

// bundleLayout is what goes into the bundle, ordered by the name each
// file takes inside it.
//
// Sorted rather than left in the order written below, so the archive's
// order is a property of the bundle rather than of how this literal
// happens to be arranged.
//
// A manifest picks a binary by platform and has no key for the
// architecture, so every platform it claims has to work on both. macOS
// does through the universal binary, and Windows through amd64, which
// its arm64 build runs under emulation. Linux has neither, and Claude
// Desktop for Linux ships x64 and arm64 both, so the bundle carries both
// Linux binaries and a launcher that picks between them at start.
func bundleLayout(dist string) ([]bundleFile, error) {
	files := []bundleFile{
		{name: serverDir + "linux-launch.sh", from: launcherSource},
		{name: "LICENSE", from: "LICENSE"},
		{name: "README.md", from: "README.md"},
	}
	for _, b := range []struct{ name, glob, what string }{
		{serverDir + "google-chat-mcp", "*darwin_all*/google-chat-mcp", "darwin universal binary"},
		{serverDir + "google-chat-mcp.exe", "*windows_amd64*/google-chat-mcp.exe", "windows amd64 binary"},
		{serverDir + "google-chat-mcp-amd64", "*linux_amd64*/google-chat-mcp", "linux amd64 binary"},
		{serverDir + "google-chat-mcp-arm64", "*linux_arm64*/google-chat-mcp", "linux arm64 binary"},
	} {
		from, err := only(dist, b.glob, b.what)
		if err != nil {
			return nil, err
		}
		files = append(files, bundleFile{name: b.name, from: from})
	}
	slices.SortFunc(files, func(a, b bundleFile) int { return cmp.Compare(a.name, b.name) })
	return files, nil
}

// only is the one file under dist matching the glob.
//
// One match or nothing: the layout under dist/ carries the build id and
// the amd64 variant, and a glob that quietly matched two would pack
// whichever sorted first.
func only(dist, glob, what string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dist, glob))
	if err != nil {
		return "", fmt.Errorf("looking for the %s: %w", what, err)
	}
	matches = slices.DeleteFunc(matches, func(p string) bool {
		info, err := os.Stat(p)
		return err != nil || !info.Mode().IsRegular()
	})
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one %s under %s, found %d%s",
			what, dist, len(matches), listing(matches))
	}
	return matches[0], nil
}

// listing spells out what was found, when anything was.
func listing(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	return ": " + strings.Join(matches, " ")
}

// checkManifest holds the manifest to the tree about to be packed, and
// reports everything wrong rather than the first thing.
func checkManifest(m bundleManifest, files []bundleFile) []string {
	packed := make(map[string]bool, len(files))
	for _, f := range files {
		packed[f.name] = true
	}

	var problems []string
	switch {
	case m.Server.EntryPoint == "":
		problems = append(problems, "the manifest declares no entry_point")
	case !packed[m.Server.EntryPoint]:
		problems = append(problems, fmt.Sprintf(
			"entry_point names %s, which the bundle does not carry", m.Server.EntryPoint))
	}

	// Not named `commands`: that is the command registry in main.go, and
	// shadowing it here would let a later edit reach for the registry
	// and silently get this instead.
	byPlatform := m.commands()
	for _, platform := range slices.Sorted(maps.Keys(byPlatform)) {
		command := byPlatform[platform]
		rel, ok := strings.CutPrefix(command, dirnameRef)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf(
				"the %s command is %q, which is not under %s, so it names a file outside the bundle",
				platform, command, dirnameRef))
		case !packed[rel]:
			problems = append(problems, fmt.Sprintf(
				"the %s command names %s, which the bundle does not carry", platform, rel))
		}
	}

	for _, key := range m.references() {
		if _, ok := m.UserConfig[key]; !ok {
			problems = append(problems, fmt.Sprintf(
				"${user_config.%s} is substituted into the server's configuration and user_config does not declare it", key))
		}
	}
	return problems
}

// writeBundle writes the entries as a deflate zip.
//
// Through a temporary file and a rename, because the next thing the
// release does is checksum whatever is in dist/, and a half-written
// bundle would be signed as readily as a whole one.
func writeBundle(out string, files []bundleFile, manifest []byte) error {
	tmp := out + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // a path built from the release's own version
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()

	if err := writeEntries(f, files, manifest); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

func writeEntries(w io.Writer, files []bundleFile, manifest []byte) error {
	zw := zip.NewWriter(w)
	add := func(name string, body io.Reader) error {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: zipEpoch}
		header.SetMode(modeFor(name))
		entry, err := zw.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := io.Copy(entry, body); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}

	// The manifest first, where an installer reading the archive in
	// order finds it before anything it describes.
	if err := add(manifestName, bytes.NewReader(manifest)); err != nil {
		return err
	}
	for _, file := range files {
		src, err := os.Open(file.from) //nolint:gosec // paths this program chose
		if err != nil {
			return err
		}
		err = add(file.name, src)
		_ = src.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// modeFor decides the mode from the entry's name rather than from the
// file it came from.
//
// The source mode is not the answer: goreleaser leaves a binary
// executable on Linux and the Windows build has no such bit to carry,
// and a checkout on a filesystem without modes has none at all.
// Everything under server/ is something the bundle runs.
func modeFor(name string) fs.FileMode {
	if strings.HasPrefix(name, serverDir) {
		return 0o755
	}
	return 0o644
}

// renderManifest is the bundle manifest with a real version in it.
//
// Decoded, edited as text, then decoded again. The first decode is what
// makes this more than a text substitution: the file has to be JSON, and
// it has to be carrying the placeholder, which is what a manifest that
// skipped this step does not. The second proves the edit left JSON
// behind, carrying the version asked for.
//
// The edit itself is on the bytes rather than on the decoded value,
// because re-encoding a map is not a neutral act. Go sorts map keys, so
// the shipped manifest would come out alphabetised — unreviewable
// against the source — and its encoder escapes `<`, `>` and `&`, so the
// `<that file>` in the long description would ship as `\u003cthat
// file\u003e`. Both are legal JSON and neither is what anyone wrote.
func renderManifest(version, path string) ([]byte, error) {
	semver := strings.TrimPrefix(version, "v")
	if semver == "" {
		return nil, fmt.Errorf("empty version")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a path the release passes in
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if manifest.Version != placeholderVersion {
		return nil, fmt.Errorf("%s carries version %q, expected the placeholder %q",
			path, manifest.Version, placeholderVersion)
	}

	// Exactly one, or the edit is ambiguous and the wrong string could
	// be the one replaced.
	quoted := []byte(`"` + placeholderVersion + `"`)
	if n := bytes.Count(raw, quoted); n != 1 {
		return nil, fmt.Errorf("%s spells %s %d times; the substitution needs exactly one",
			path, quoted, n)
	}
	out := bytes.Replace(raw, quoted, []byte(`"`+semver+`"`), 1)

	var check struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("%s: the substitution did not leave valid JSON: %w", path, err)
	}
	if check.Version != semver {
		return nil, fmt.Errorf("%s: the substitution set version to %q, wanted %q",
			path, check.Version, semver)
	}
	// The manifest is a text file, and the source ends with a newline.
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, nil
}

// mcpbManifest prints the bundle manifest with the release's version in
// it.
func mcpbManifest(version, manifestPath string, stdout, stderr io.Writer) int {
	out, err := renderManifest(version, manifestPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "mcpb-manifest:", err)
		return 1
	}
	_, _ = fmt.Fprint(stdout, string(out))
	return 0
}

// bundleManifest is the part of the manifest these gates read.
type bundleManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Server      struct {
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

// commands is every place the manifest names a file to run, keyed by the
// platform it applies to.
func (m bundleManifest) commands() map[string]string {
	out := map[string]string{"default": m.Server.MCPConfig.Command}
	for platform, o := range m.Server.MCPConfig.PlatformOverrides {
		if o.Command != "" {
			out[platform] = o.Command
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
