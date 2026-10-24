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
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/mmedum/google-chat-mcp/v7/internal/config"
	"github.com/mmedum/google-chat-mcp/v7/internal/server"
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
// to be packed, and the schema against a vendored copy. What the bundle
// asks for is checked against the server's own settings. manifestProblems
// runs all of it, for the packer and, through the tests, on every commit.

// schemaRef is the $schema a manifest may name: anthropics/mcpb at a
// tag, never at a branch.
//
// The version in the path pins the format; the ref pins the bytes. A
// document that claims to conform to `main` claims to conform to
// whatever `main` says tomorrow, which is the same float the pins gate
// refuses in a workflow. The schema at v2.1.2 and at main were
// byte-identical when this was written — sha256 3a0ac9d8…, checked
// 2026-09-17 — so naming the tag costs nothing and fixes what is being
// conformed to.
var schemaRef = regexp.MustCompile(`^https://raw\.githubusercontent\.com/anthropics/mcpb/v\d+\.\d+\.\d+/schemas/`)

// schemaFormat is the format version in the schema's file name, which is
// the other half of what a $schema URL says: the ref pins the bytes, and
// this pins what those bytes describe.
var schemaFormat = regexp.MustCompile(`/schemas/mcpb-manifest-v(\d+\.\d+)\.schema\.json$`)

// schemaRefProblem reports a $schema that is missing or floating.
func schemaRefProblem(m bundleManifest) string {
	switch {
	case m.Schema == "":
		return "the manifest names no $schema, so nothing says which format it claims to be"
	case !schemaRef.MatchString(m.Schema):
		return fmt.Sprintf("$schema is %q, which does not name anthropics/mcpb at a tag: "+
			"a branch ref can be amended under a document that claims to conform to it", m.Schema)
	}

	// The two halves have to agree. A manifest validates against the
	// vendored copy of the schema, and that copy pins manifest_version
	// with a const — so the manifest cannot drift from the copy. It can
	// drift from its own URL: point $schema at v0.4 while declaring 0.3
	// and every other check here still passes, because nothing asks the
	// copy about the URL it came from. The Pipedrive server checks this
	// directly, and it is the claim this gate was missing.
	format := schemaFormat.FindStringSubmatch(m.Schema)
	switch {
	case format == nil:
		return fmt.Sprintf("$schema is %q, which does not name a manifest schema file: "+
			"the version it describes cannot be read from it", m.Schema)
	case m.ManifestVersion == "":
		return "the manifest declares no manifest_version, so nothing says which format it is"
	case format[1] != m.ManifestVersion:
		return fmt.Sprintf("$schema names the %s schema and manifest_version is %q: "+
			"the document claims to be one format and points at another", format[1], m.ManifestVersion)
	}
	return ""
}

// manifestSource is the manifest as the repository carries it, with the
// placeholder version still in it.
var manifestSource = filepath.Join("packaging", "mcpb", "manifest.json")

// launcherSource is the Linux launcher.
var launcherSource = filepath.Join("packaging", "mcpb", "launch-linux.sh")

// launcherName is the launcher inside the bundle, and Linux's entry
// point.
var launcherName = serverDir + filepath.Base(launcherSource)

// goModSource is go.mod, which the launcher's install hint is held to.
const goModSource = "go.mod"

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
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "gates: mcpb-pack: %v\n", err)
		return 1
	}
	// Trimmed here for the bundle's filename; renderManifest is what
	// refuses an empty one, so the rule lives in one place.
	version := strings.TrimPrefix(args[1], "v")
	dist := cmp.Or(argAt(args, 2), "dist")

	files, err := bundleLayout(dist)
	if err != nil {
		return fail(err)
	}
	// Read once: the bytes checked are the bytes shipped.
	committed, err := os.ReadFile(manifestSource)
	if err != nil {
		return fail(err)
	}
	if problems := manifestProblems(committed, files, "."); len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "gates: mcpb-pack: "+p)
		}
		return 1
	}
	manifest, err := renderManifest(version, committed)
	if err != nil {
		return fail(err)
	}

	out := filepath.Join(dist, fmt.Sprintf("google-chat-mcp_%s.mcpb", version))
	if err := writeBundle(out, files, manifest); err != nil {
		return fail(err)
	}
	_, _ = fmt.Fprintf(stdout, "mcpb-pack: wrote %s\n", out)
	return 0
}

// manifestProblems is every check the gate and the packer both run, so
// the release packs no manifest the tests would refuse: the placeholder
// version, what the manifest says it is, the vendored schema, the tree
// about to be packed, the settings, and the launcher and go.mod beside
// it. raw is the committed manifest and files the staged layout; root
// is the repository the schema, the launcher and go.mod are read from.
// The tests are the gate, and run it on stagedNames, with no build.
func manifestProblems(raw []byte, files []bundleFile, root string) []string {
	var m bundleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return []string{fmt.Sprintf("%s: %v", manifestSource, err)}
	}
	var problems []string
	if m.Version != placeholderVersion {
		problems = append(problems, fmt.Sprintf("the manifest carries version %q; the committed one carries the "+
			"placeholder %q, which the packer replaces", m.Version, placeholderVersion))
	}
	if p := schemaRefProblem(m); p != "" {
		problems = append(problems, p)
	}
	problems = append(problems, schemaProblems(raw, root)...)
	problems = append(problems, identityProblems(m)...)
	problems = append(problems, checkManifest(m, files)...)
	if loose := alwaysSubstituted(m); len(loose) > 0 {
		problems = append(problems, "user configuration that the installing client may leave unsubstituted: "+
			strings.Join(loose, ", "))
	}
	problems = append(problems, settingProblems(m, bundleLeftOut)...)
	return append(problems, launcherProblems(files, root)...)
}

// vendoredSchema is anthropics/mcpb schemas/mcpb-manifest-v0.3.schema.json,
// fetched 2026-09-06, byte-identical to that repository's "latest"
// schema then.
var vendoredSchema = filepath.Join("scripts", "gates", "testdata", "mcpb-manifest-v0.3.schema.json")

// schemaProblems validates the manifest against the vendored copy of its
// published schema. Each side has a floor on what was read: an empty
// document validates against a schema whose fields are all optional, and
// a stub schema validates anything.
func schemaProblems(raw []byte, root string) []string {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []string{fmt.Sprintf("%s: %v", manifestSource, err)}
	}
	if len(doc) < 10 {
		return []string{fmt.Sprintf("the manifest has %d top-level fields, expected the full one", len(doc))}
	}
	path := filepath.Join(root, vendoredSchema)
	src, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		return []string{err.Error()}
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(src, &schema); err != nil {
		return []string{fmt.Sprintf("%s: %v", path, err)}
	}
	if len(schema.Properties) < 20 {
		return []string{fmt.Sprintf("the vendored schema has %d properties, expected the published one", len(schema.Properties))}
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", path, err)}
	}
	if err := resolved.Validate(doc); err != nil {
		return []string{fmt.Sprintf("%s does not match the 0.3 schema: %v", manifestSource, err)}
	}
	return nil
}

// claudeDesktopFloor is the Claude Desktop version every bundle in the
// family claims.
const claudeDesktopFloor = ">=0.10.0"

// identityProblems holds what the manifest says it is to the server and
// to the family: the server's name and description, a binary, the
// family's Claude Desktop floor, and all three platforms Claude Desktop
// runs on. checkManifest holds each platform to the file staged for it.
func identityProblems(m bundleManifest) []string {
	var problems []string
	if m.Name != server.Name {
		problems = append(problems, fmt.Sprintf("manifest name %q, server name %q", m.Name, server.Name))
	}
	if m.Description != server.Description {
		problems = append(problems, fmt.Sprintf("manifest description does not match server.Description:\n%q\n%q",
			m.Description, server.Description))
	}
	if m.Server.Type != "binary" {
		problems = append(problems, fmt.Sprintf("server type %q, expected binary", m.Server.Type))
	}
	if m.Compatibility.ClaudeDesktop != claudeDesktopFloor {
		problems = append(problems, fmt.Sprintf("compatibility.claude_desktop is %q, want the family's %s",
			m.Compatibility.ClaudeDesktop, claudeDesktopFloor))
	}
	if got := m.Compatibility.Platforms; !slices.Equal(got, []string{"darwin", "linux", "win32"}) {
		problems = append(problems, fmt.Sprintf("compatibility platforms %v, expected darwin, linux and win32", got))
	}
	return problems
}

// alwaysSubstituted names the references the installing client may leave
// standing. A setting that is neither required nor defaulted is one the
// person installing can skip, and the reference host then hands the
// server the literal ${user_config.key}, which starts it with nonsense
// in its configuration, so the manifest may not have any. A reference
// user_config does not declare is checkManifest's to report.
func alwaysSubstituted(m bundleManifest) []string {
	var loose []string
	for _, key := range m.references() {
		if cfg, ok := m.UserConfig[key]; ok && !cfg.Required && cfg.Default == nil {
			loose = append(loose, key+" (optional, no default)")
		}
	}
	return loose
}

var (
	// launcherBinary is a binary the launcher picks.
	launcherBinary = regexp.MustCompile(`bin=\$dir/(\S+)`)
	// moduleLine is go.mod's module directive. Trailing space is allowed
	// because a checkout with Windows line endings ends it with "\r".
	moduleLine  = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)
	installHint = regexp.MustCompile(`go install [^\s"]+`)
)

// launcherProblems holds the Linux launcher to the packer. It picks
// between binaries no manifest names, so renaming a staged one would
// pass every check above and ship a bundle that starts nothing on
// Linux. Its install hint, for an architecture the bundle does not
// carry, names the module go.mod declares, which moved at v6 and was
// left behind once.
func launcherProblems(files []bundleFile, root string) []string {
	script, err := os.ReadFile(filepath.Join(root, launcherSource)) //nolint:gosec // a path this repository owns
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	var staged []string
	for _, f := range files {
		if strings.HasPrefix(f.name, serverDir+server.Name+"-linux-") {
			staged = append(staged, strings.TrimPrefix(f.name, serverDir))
		}
	}
	var named []string
	for _, m := range launcherBinary.FindAllStringSubmatch(string(script), -1) {
		named = append(named, m[1])
	}
	slices.Sort(named)
	if len(staged) != 2 || !slices.Equal(named, staged) {
		problems = append(problems, fmt.Sprintf("the launcher runs %v and the packer stages %v", named, staged))
	}

	goMod, err := os.ReadFile(filepath.Join(root, goModSource)) //nolint:gosec // a path this repository owns
	if err != nil {
		return append(problems, err.Error())
	}
	mod := moduleLine.FindStringSubmatch(string(goMod))
	if mod == nil {
		return append(problems, "go.mod names no module")
	}
	want := "go install " + mod[1] + "/cmd/" + server.Name + "@latest"
	hints := installHint.FindAllString(string(script), -1)
	if len(hints) == 0 {
		return append(problems, "the launcher gives no install hint for an architecture the bundle does not carry")
	}
	for _, hint := range hints {
		if hint != want {
			problems = append(problems, fmt.Sprintf("the launcher says %q, want %q", hint, want))
		}
	}
	return problems
}

// staged is one built file going into the bundle: where to find it under
// dist, what it is called inside, and the manifest platform it is the
// entry point for, or empty for one carried but never spawned.
type staged struct {
	glob, as, what string
	platform       string
}

// binaries are the four built files the bundle carries.
//
// A manifest picks a binary by platform and has no key for the
// architecture, so every platform it claims has to work on both. macOS
// does through the universal binary, and Windows through amd64, which
// its arm64 build runs under emulation. Linux has neither, and Claude
// Desktop for Linux ships x64 and arm64 both, so the bundle carries both
// Linux binaries and a launcher that picks between them at start.
var binaries = []staged{
	{"*darwin_all*/google-chat-mcp", serverDir + "google-chat-mcp-darwin", "darwin universal binary", "darwin"},
	{"*windows_amd64*/google-chat-mcp.exe", serverDir + "google-chat-mcp.exe", "windows amd64 binary", "win32"},
	// Neither Linux binary is an entry point: the launcher is, and it
	// picks between these two from `uname -m`.
	{"*linux_amd64*/google-chat-mcp", serverDir + "google-chat-mcp-linux-amd64", "linux amd64 binary", ""},
	{"*linux_arm64*/google-chat-mcp", serverDir + "google-chat-mcp-linux-arm64", "linux arm64 binary", ""},
}

// carried are the files the bundle takes from the repository rather
// than from the build.
var carried = []bundleFile{
	{name: launcherName, from: launcherSource},
	{name: "LICENSE", from: "LICENSE"},
	{name: "README.md", from: "README.md"},
}

// entryPointFor is the file a platform spawns: its own binary, or the
// launcher where the platform needs one binary per architecture.
func entryPointFor(platform string) string {
	if platform == "linux" {
		return launcherName
	}
	for _, b := range binaries {
		if b.platform == platform {
			return b.as
		}
	}
	return ""
}

// bundleLayout is what goes into the bundle, ordered by the name each
// file takes inside it.
//
// Sorted rather than left in the order written above, so the archive's
// order is a property of the bundle rather than of how those literals
// happen to be arranged.
func bundleLayout(dist string) ([]bundleFile, error) {
	files := slices.Clone(carried)
	for _, b := range binaries {
		from, err := only(dist, b.glob, b.what)
		if err != nil {
			return nil, err
		}
		files = append(files, bundleFile{name: b.as, from: from})
	}
	slices.SortFunc(files, func(a, b bundleFile) int { return cmp.Compare(a.name, b.name) })
	return files, nil
}

// stagedNames is the layout with no build: the names bundleLayout gives,
// the built files with no source. The checks are referential, so the
// names are all they read.
func stagedNames() []bundleFile {
	files := slices.Clone(carried)
	for _, b := range binaries {
		files = append(files, bundleFile{name: b.as})
	}
	slices.SortFunc(files, func(a, b bundleFile) int { return cmp.Compare(a.name, b.name) })
	return files
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
	// programs is what can be run: everything under server/. LICENSE and
	// README.md are packed to be read, and nothing may spawn them.
	programs := make(map[string]bool, len(files))
	for _, f := range files {
		if strings.HasPrefix(f.name, serverDir) {
			programs[f.name] = true
		}
	}

	var problems []string
	switch {
	case m.Server.EntryPoint == "":
		problems = append(problems, "the manifest declares no entry_point")
	case !programs[m.Server.EntryPoint]:
		problems = append(problems, fmt.Sprintf(
			"entry_point names %s, which is not a program the bundle carries", m.Server.EntryPoint))
	}

	// Not named `commands`: that is the command registry in main.go, and
	// shadowing it here would let a later edit reach for the registry
	// and silently get this instead.
	byPlatform := m.commands()
	// runs is the program each command spawns. A command that names
	// none is reported here and left out, so the platform check below
	// does not report it again.
	runs := map[string]string{}
	for _, platform := range slices.Sorted(maps.Keys(byPlatform)) {
		command := byPlatform[platform]
		// ${__dirname} is the one path the specification fixes to the
		// bundle. It shows a relative command once and never says what it
		// is resolved against, and the reference host resolves no path
		// (docs/architecture.md, the evidence log).
		rel, ok := strings.CutPrefix(command, dirnameRef)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf(
				"the %s command is %q; a command starts with %s, the one path the specification fixes to the bundle",
				platform, command, dirnameRef))
		case !programs[rel]:
			problems = append(problems, fmt.Sprintf(
				"the %s command names %s, which is not a program the bundle carries", platform, rel))
		default:
			runs[platform] = rel
		}
	}

	// An override for a platform the bundle does not claim reaches
	// nobody: the host picks an override by platform.
	claimed := m.Compatibility.Platforms
	for _, platform := range slices.Sorted(maps.Keys(m.Server.MCPConfig.PlatformOverrides)) {
		if len(claimed) > 0 && !slices.Contains(claimed, platform) {
			problems = append(problems, fmt.Sprintf(
				"platform_overrides.%s is an override for a platform compatibility.platforms does not claim (%s), "+
					"so nothing will ever use it", platform, strings.Join(claimed, ", ")))
		}
	}
	// Every platform the bundle claims spawns the file staged for it, not
	// merely a staged program: with no win32 override, Windows would run
	// the default command, the macOS binary, which really is in the
	// bundle.
	for _, platform := range claimed {
		want := entryPointFor(platform)
		if want == "" {
			problems = append(problems, fmt.Sprintf(
				"compatibility.platforms claims %s and the packer stages no entry point for it", platform))
			continue
		}
		from := "default"
		if _, ok := byPlatform[platform]; ok {
			from = platform
		}
		if got, ok := runs[from]; ok && got != want {
			problems = append(problems, fmt.Sprintf(
				"on %s the bundle would spawn %q, and the file staged for %s is %q. Give %s a "+
					"platform_overrides entry, or make the default command name %q",
				platform, got, platform, want, platform, want))
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
	return writeThrough(out, func(w io.Writer) error { return writeEntries(w, files, manifest) })
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

// renderManifest is the committed manifest, raw, with a real version in
// it. raw is the bytes manifestProblems checked, which holds them to the
// placeholder, so the bytes checked are the bytes shipped.
//
// Edited as text, then decoded, which proves the edit left JSON behind,
// carrying the version asked for.
//
// The edit itself is on the bytes rather than on a decoded value,
// because re-encoding a map is not a neutral act. Go sorts map keys, so
// the shipped manifest would come out alphabetized — unreviewable
// against the source — and its encoder escapes `<`, `>` and `&`, so the
// `<that file>` in the long description would ship as `\u003cthat
// file\u003e`. Both are legal JSON and neither is what anyone wrote.
func renderManifest(version string, raw []byte) ([]byte, error) {
	semver := strings.TrimPrefix(version, "v")
	if semver == "" {
		return nil, fmt.Errorf("empty version")
	}

	// Exactly one, or the edit is ambiguous and the wrong string could
	// be the one replaced.
	quoted := []byte(`"` + placeholderVersion + `"`)
	if n := bytes.Count(raw, quoted); n != 1 {
		return nil, fmt.Errorf("the manifest spells %s %d times; the substitution needs exactly one", quoted, n)
	}
	out := bytes.Replace(raw, quoted, []byte(`"`+semver+`"`), 1)

	var check struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("the substitution did not leave valid JSON: %w", err)
	}
	if check.Version != semver {
		return nil, fmt.Errorf("the substitution set version to %q, wanted %q", check.Version, semver)
	}
	// The manifest is a text file, and the source ends with a newline.
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, nil
}

// mcpbManifest prints the bundle manifest with the release's version in
// it, once it passes every check the packer runs, on the names the
// packer stages. It runs from the repository root, which the checks
// read the schema, the launcher and go.mod from.
func mcpbManifest(version, manifestPath string, stdout, stderr io.Writer) int {
	raw, err := os.ReadFile(manifestPath) //nolint:gosec // a path the maintainer passes in
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "mcpb-manifest:", err)
		return 1
	}
	if problems := manifestProblems(raw, stagedNames(), "."); len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "mcpb-manifest: "+p)
		}
		return 1
	}
	out, err := renderManifest(version, raw)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "mcpb-manifest:", err)
		return 1
	}
	_, _ = fmt.Fprint(stdout, string(out))
	return 0
}

// bundleManifest is the part of the manifest these gates read.
type bundleManifest struct {
	Schema          string   `json:"$schema"`
	ManifestVersion string   `json:"manifest_version"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Description     string   `json:"description"`
	PrivacyPolicies []string `json:"privacy_policies"`
	Server          struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command string `json:"command"`
			// Args is raw, so that a key set to null is told apart from
			// one that is missing.
			Args              json.RawMessage   `json:"args"`
			Env               map[string]string `json:"env"`
			PlatformOverrides map[string]struct {
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			} `json:"platform_overrides"`
		} `json:"mcp_config"`
	} `json:"server"`
	UserConfig map[string]struct {
		Type        string `json:"type"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Required    bool   `json:"required"`
		Default     any    `json:"default"`
	} `json:"user_config"`
	Compatibility struct {
		ClaudeDesktop string   `json:"claude_desktop"`
		Platforms     []string `json:"platforms"`
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
// command, an argument or an environment variable. Arguments included,
// though settingProblems refuses one, so that each check reading this
// holds on its own.
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
	collect(m.args()...)
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

// args is mcp_config.args as strings: none when the key is missing,
// null or not a list of strings. settingProblems reports the first two,
// and the schema test the third.
func (m bundleManifest) args() []string {
	var args []string
	if err := json.Unmarshal(m.Server.MCPConfig.Args, &args); err != nil {
		return nil
	}
	return args
}

// Why bundleLeftOut leaves a setting out.
const (
	diagnostics = "diagnostics, set by hand while chasing a problem"
	tuning      = "tuning, whose default suits an install"
	localServer = "a development override, for a test pointing at a local server"
)

// bundleLeftOut is every setting the server reads that the bundle does
// not ask for, with why. A removed setting is left out without an
// entry, because config says which it is.
var bundleLeftOut = map[string]string{
	config.EnvPrefix + "LOG_LEVEL":                   diagnostics,
	config.EnvPrefix + "LOG_FORMAT":                  diagnostics,
	config.EnvPrefix + "HTTP_TIMEOUT_SECONDS":        tuning,
	config.EnvPrefix + "HTTP_MAX_RETRIES":            tuning,
	config.EnvPrefix + "SEARCH_MAX_PAGES":            tuning,
	config.EnvPrefix + "DIRECTORY_CACHE_TTL_SECONDS": tuning,
	config.EnvPrefix + "CHAT_API_BASE":               localServer,
	config.EnvPrefix + "PEOPLE_API_BASE":             localServer,
	config.EnvPrefix + "CLOUD_IDENTITY_API_BASE":     localServer,
	config.EnvConfigDir:                              "a development override, which tests move the profile with",
	config.EnvConfigDirAllowOutsideHome:              "goes with " + config.EnvConfigDir,
	config.EnvRefreshToken:                           "a credential for automation; a bundle install uses the login's keyring entry",
}

// bundleVarsFrom is the settings the bundle asks for at install: every
// variable the server reads, less the removed ones and less each one
// leftOut gives a reason for. A setting added later is asked for until
// the manifest asks for it or a reason is written. It also refuses an
// exclusion the server has no use for: one naming a variable it does
// not read, or one config already says is removed. vars is config's
// settings by name.
func bundleVarsFrom(read []string, vars map[string]config.Var, leftOut map[string]string) (asked, problems []string) {
	for _, name := range read {
		if !vars[name].Removed && leftOut[name] == "" {
			asked = append(asked, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(leftOut)) {
		switch {
		case !slices.Contains(read, name):
			problems = append(problems, fmt.Sprintf("bundleLeftOut names %s, which the server does not read", name))
		case vars[name].Removed:
			problems = append(problems, fmt.Sprintf("bundleLeftOut names %s, which a release removed, "+
				"and so left out already", name))
		}
	}
	return asked, problems
}

// bundleKey is the user_config key a setting is asked under: its name
// without the prefix, lowercased, which is what gives the sibling
// servers' bundles the same keys.
func bundleKey(name string) string {
	return strings.ToLower(strings.TrimPrefix(name, config.EnvPrefix))
}

// fieldRule is the exact words a setting's field must carry, since the
// server refuses anything else at start, and what a field without them
// fails to do.
type fieldRule struct{ words, fails string }

// fieldRules are the settings whose field says what the server accepts.
// The toolsets are the whole list, in order: a word check passes on
// "settings" or "events" anywhere in the prose.
var fieldRules = map[string]fieldRule{
	config.EnvPrefix + "PROFILE":  {words: config.ProfileRule, fails: "say a profile name is"},
	config.EnvPrefix + "TOOLSETS": {words: config.ToolsetList(), fails: "list the toolsets as"},
}

// settingProblems holds what the bundle asks for to the server's own
// settings, and reports everything wrong rather than the first thing.
//
// Every setting bundleVarsFrom derives is asked for and reaches its
// variable, and nothing else is. A switch is asked as a boolean, so
// Claude Desktop shows it as one. The environment the defaults give the
// server configures it as no environment does: a bundle install may
// turn on nothing an unconfigured server leaves off, because in Claude
// Desktop the only other guard is its approval of each tool, which a
// person can set to always allow. The sets of switches the server
// refuses together are the ones bundleRefused declares. A platform
// override carries no env, because the reference host replaces
// mcp_config.env with it rather than merging the two, and these checks
// read mcp_config.env alone. mcp_config.args is there and empty: the
// bundle passes every setting by env, an argument such as
// --enable-destructive=true would configure the server past these
// checks, and no Claude Desktop launch has shown the key optional. A
// platform override carries no args, an empty list included, since the
// host replaces mcp_config.args with it the same way. The profile and
// toolsets fields say what the server accepts, and a bundle that asks
// for neither is refused, so that check cannot go quiet. And
// privacy_policies is not empty, which the manifest specification
// requires of a server that talks to a service processing user data,
// and which neither the schema nor `mcpb validate` enforces. leftOut is
// bundleLeftOut, or a test's.
func settingProblems(m bundleManifest, leftOut map[string]string) []string {
	vars := map[string]config.Var{}
	for _, v := range config.Vars() {
		vars[v.Name] = v
	}
	bundleVars, problems := bundleVarsFrom(config.EnvVars(), vars, leftOut)

	if len(m.PrivacyPolicies) == 0 {
		problems = append(problems, "privacy_policies is empty; the manifest specification requires one for a "+
			"server that connects to a service processing user data, as this one does to Google")
	}
	const keepArgs = "keep it as [], since no Claude Desktop launch has shown the key optional"
	switch raw, args := m.Server.MCPConfig.Args, m.args(); {
	case raw == nil:
		problems = append(problems, "mcp_config.args is missing; "+keepArgs)
	case string(raw) == "null":
		problems = append(problems, "mcp_config.args is null; "+keepArgs)
	case len(args) > 0:
		problems = append(problems, fmt.Sprintf("mcp_config.args is %q; the bundle passes every setting "+
			"through mcp_config.env, and an argument would configure the server past these checks", args))
	}
	for _, platform := range slices.Sorted(maps.Keys(m.Server.MCPConfig.PlatformOverrides)) {
		o := m.Server.MCPConfig.PlatformOverrides[platform]
		if o.Env != nil {
			problems = append(problems, fmt.Sprintf("platform_overrides.%s.env is set; a host replaces "+
				"mcp_config.env with it, an empty one too, so put every variable in mcp_config.env", platform))
		}
		if o.Args != nil {
			problems = append(problems, fmt.Sprintf("platform_overrides.%s.args is set; a host replaces "+
				"mcp_config.args with it, an empty one too, and the bundle passes every setting through mcp_config.env",
				platform))
		}
	}
	var switches []string
	asked := map[string]bool{}
	for _, name := range bundleVars {
		key := bundleKey(name)
		asked[key] = true
		opt, declared := m.UserConfig[key]
		if !declared {
			problems = append(problems, fmt.Sprintf("user_config does not ask for %s, which sets %s", key, name))
			continue
		}
		if got, want := m.Server.MCPConfig.Env[name], "${user_config."+key+"}"; got != want {
			problems = append(problems, fmt.Sprintf("mcp_config.env.%s is %q, want %q", name, got, want))
		}
		if vars[name].Switch {
			switches = append(switches, key)
			if opt.Type != "boolean" {
				problems = append(problems, fmt.Sprintf("user_config.%s is a %s; %s is a switch, so ask for a boolean",
					key, cmp.Or(opt.Type, "field with no type"), name))
			}
		}
		if rule, ok := fieldRules[name]; ok && !strings.Contains(opt.Description, rule.words) {
			problems = append(problems, fmt.Sprintf("user_config.%s does not %s %q", key, rule.fails, rule.words))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(fieldRules)) {
		if !slices.Contains(bundleVars, name) {
			problems = append(problems, fmt.Sprintf("the bundle asks for no %s, so nothing holds its field to %q",
				name, fieldRules[name].words))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(m.UserConfig)) {
		if !asked[key] {
			problems = append(problems, fmt.Sprintf("user_config asks for %s, which is no setting's key", key))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(m.Server.MCPConfig.Env)) {
		v := vars[name]
		switch {
		case slices.Contains(bundleVars, name):
		case v.Removed:
			problems = append(problems, fmt.Sprintf("mcp_config.env sets %s, which a release removed", name))
		case leftOut[name] != "":
			problems = append(problems, fmt.Sprintf("mcp_config.env sets %s, which the bundle leaves out: %s",
				name, leftOut[name]))
		default:
			problems = append(problems, fmt.Sprintf("mcp_config.env sets %s, which the server does not read", name))
		}
	}
	slices.Sort(switches)
	defaults, defaultsErr := defaultsProblems(m)
	problems = append(problems, defaults...)
	return append(problems, refusalProblems(m, switches, bundleRefused, defaultsErr)...)
}

// hostEnv is the environment the reference host builds from the
// bundle's defaults, with each key in on set to true whatever its
// default, as the person turning it on would. A boolean becomes "true"
// or "false", a "" default stays "", a number is written out, and a
// reference with no default is passed through as written.
//
// A variable spending a required key with no default is left out, since
// the person supplies it and it is theirs either way. One with a default
// is not: the reference host starts the server only with the person's
// own value for it, but Claude Desktop's form may fill that in from the
// default, and a person may accept it unread; the evidence log has the
// source. A default that is a list or an object is not guessed at: its
// variable is left out, and its key is in unreadable with what it is.
//
// It reads mcp_config.env alone. That is sound only because
// settingProblems refuses an argument and an override's env; relaxing
// either needs this to read the same (written down from a /code-review
// high, in docs/architecture.md).
func hostEnv(m bundleManifest, on ...string) (env, unreadable map[string]string) {
	env, unreadable = map[string]string{}, map[string]string{}
	for name, value := range m.Server.MCPConfig.Env {
		var b strings.Builder
		skip, last := false, 0
		for _, at := range userConfigRef.FindAllStringSubmatchIndex(value, -1) {
			key, sub := value[at[2]:at[3]], value[at[0]:at[1]]
			opt := m.UserConfig[key]
			def := opt.Default
			if slices.Contains(on, key) {
				def = true
			}
			switch d := def.(type) {
			case bool:
				sub = strconv.FormatBool(d)
			case string:
				sub = d
			case float64:
				sub = strconv.FormatFloat(d, 'f', -1, 64)
			case nil:
				skip = skip || opt.Required
			case []any:
				unreadable[key], skip = "a list", true
			case map[string]any:
				unreadable[key], skip = "an object", true
			}
			b.WriteString(value[last:at[0]] + sub)
			last = at[1]
		}
		if !skip {
			env[name] = b.String() + value[last:]
		}
	}
	return env, unreadable
}

// loadEnv starts the server's configuration from an environment alone.
func loadEnv(env map[string]string) (config.Config, error) {
	return config.Load(nil, func(name string) string { return env[name] })
}

// defaultsProblems reports a bundle whose defaults configure the server
// otherwise than no environment does, naming the fields that differ. A
// default that is a list or an object is reported rather than guessed
// at, every one in key order, and the comparison still runs over the
// rest. defaultsErr is what the server refuses the defaults with, which
// decides whether refusalProblems starts the sets of switches.
func defaultsProblems(m bundleManifest) (problems []string, defaultsErr error) {
	env, unreadable := hostEnv(m)
	for _, key := range slices.Sorted(maps.Keys(unreadable)) {
		problems = append(problems, fmt.Sprintf("user_config.%s defaults to %s, which the defaults check cannot "+
			"build an environment from: give it a boolean, a string or a number", key, unreadable[key]))
	}
	got, defaultsErr := loadEnv(env)
	if defaultsErr != nil {
		problems = append(problems, fmt.Sprintf("the environment the bundle's defaults give the server is refused: %v",
			defaultsErr))
		return problems, defaultsErr
	}
	want, err := loadEnv(nil)
	if err != nil {
		return append(problems, fmt.Sprintf("an unconfigured server is refused: %v", err)), nil
	}
	if diff := configDiff(got, want); len(diff) > 0 {
		problems = append(problems, fmt.Sprintf("the bundle's defaults configure the server otherwise than no "+
			"environment does, in %s; a bundle install may turn on nothing an unconfigured server leaves off",
			strings.Join(diff, ", ")))
	}
	return problems, nil
}

// configDiff names the fields two configurations disagree on.
func configDiff(a, b config.Config) []string {
	return fieldDiff(a, b)
}

// fieldDiff names the fields of two values of one struct type that
// differ. An unexported field cannot be read out through Interface, so
// it is compared with Value.Equal where its type compares; one that does
// not, and differs, is reported unnamed.
func fieldDiff(a, b any) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	var fields []string
	unnamed := false
	for i := range va.NumField() {
		f := va.Type().Field(i)
		switch {
		case f.IsExported():
			if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
				fields = append(fields, f.Name)
			}
		case f.Type.Comparable():
			if !va.Field(i).Equal(vb.Field(i)) {
				fields = append(fields, f.Name)
			}
		default:
			unnamed = true
		}
	}
	if unnamed && len(fields) == 0 && !reflect.DeepEqual(a, b) {
		fields = append(fields, "an unexported field")
	}
	return fields
}

// bundleRefused are the sets of the bundle's switches the server refuses
// at start, each sorted. Each field in one names the others' titles in
// its description. Build refuses none today: read-only leaves the
// destructive tools unregistered rather than refusing the pair. The gate
// starts every set of the switches and holds the refused ones to this
// list, both ways, so a refusal added to Build fails here until it is
// declared and the fields say so.
var bundleRefused [][]string

// refusalProblems starts every set of the bundle's switches, each on
// over the defaults, and holds the ones the server refuses to declared:
// a refusal it does not declare, which no description then warns of,
// and a declared one the server starts with, each reported for the
// smallest set that shows it. switches is the user_config key of every
// switch the bundle asks for, sorted. A declared key that is not one of
// them is refused too, and so is one with no title, which the others'
// descriptions would name, each once. When the defaults alone do not
// start, which defaultsErr says, it starts no set: the defaults check
// reports that, and every set would fail with it.
func refusalProblems(m bundleManifest, switches []string, declared [][]string, defaultsErr error) []string {
	problems := declaredProblems(m, switches, declared)
	if defaultsErr != nil {
		return problems
	}
	return append(problems, startProblems(m, switches, declared)...)
}

// declaredProblems holds each key in a declared set to a switch with a
// title, each once, and each switch's description to the titles of the
// others in its sets, trimmed.
func declaredProblems(m bundleManifest, switches []string, declared [][]string) []string {
	title := func(key string) string { return strings.TrimSpace(m.UserConfig[key].Title) }
	var problems []string
	inSets := map[string]bool{}
	for _, set := range declared {
		for _, key := range set {
			inSets[key] = true
		}
	}
	titled := map[string]bool{}
	for _, key := range slices.Sorted(maps.Keys(inSets)) {
		switch {
		case !slices.Contains(switches, key):
			problems = append(problems, fmt.Sprintf("bundleRefused names %s, which is not a switch the bundle asks for", key))
		case title(key) == "":
			problems = append(problems, fmt.Sprintf("user_config.%s has no title, which the fields the server "+
				"refuses it with must name", key))
		default:
			titled[key] = true
		}
	}
	for _, set := range declared {
		for _, key := range set {
			if !slices.Contains(switches, key) {
				continue
			}
			description := m.UserConfig[key].Description
			for _, other := range set {
				if other != key && titled[other] && !strings.Contains(description, title(other)) {
					problems = append(problems, fmt.Sprintf("user_config.%s does not name %q, which the server "+
						"refuses it with", key, title(other)))
				}
			}
		}
	}
	return problems
}

// startProblems starts every set of the switches over the defaults and
// holds the server's refusals to declared, both ways. Every subset of a
// set comes before it in this order, so the first set to show a defect
// is the smallest, and a superset is not reported again.
func startProblems(m bundleManifest, switches []string, declared [][]string) []string {
	var problems []string
	var undeclared []int
	startsDeclared := map[int]bool{}
	for bits := 1; bits < 1<<len(switches); bits++ {
		var on []string
		for i, key := range switches {
			if bits&(1<<i) != 0 {
				on = append(on, key)
			}
		}
		env, _ := hostEnv(m, on...)
		_, err := loadEnv(env)
		var within []int
		for i, set := range declared {
			if !slices.ContainsFunc(set, func(key string) bool { return !slices.Contains(on, key) }) {
				within = append(within, i)
			}
		}
		switch {
		case err != nil && len(within) == 0:
			if slices.ContainsFunc(undeclared, func(shown int) bool { return shown&bits == shown }) {
				continue
			}
			undeclared = append(undeclared, bits)
			problems = append(problems, fmt.Sprintf("the server refuses %s together, and bundleRefused does not "+
				"declare it, so no field's description warns of it: %v", strings.Join(on, ", "), err))
		case err == nil:
			for _, i := range within {
				if !startsDeclared[i] {
					startsDeclared[i] = true
					problems = append(problems, fmt.Sprintf("bundleRefused declares that the server refuses %s "+
						"together, and it starts with them", strings.Join(declared[i], ", ")))
				}
			}
		}
	}
	return problems
}
