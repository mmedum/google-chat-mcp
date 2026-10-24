package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"encoding/json"
	"flag"
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

	"github.com/mmedum/google-chat-mcp/v7/internal/config"
	"github.com/mmedum/google-chat-mcp/v7/internal/credentials"
	"github.com/mmedum/google-chat-mcp/v7/internal/userconfig"
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
// What the bundle asks for is checked against the server's own
// settings, by the packer and on every commit.

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
	if p := schemaRefProblem(m); p != "" {
		return fail("%s", p)
	}
	problems := append(checkManifest(m, files), settingProblems(m, bundleLeftOut)...)
	if len(problems) > 0 {
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
		{name: serverDir + "launch-linux.sh", from: launcherSource},
		{name: "LICENSE", from: "LICENSE"},
		{name: "README.md", from: "README.md"},
	}
	for _, b := range []struct{ name, glob, what string }{
		{serverDir + "google-chat-mcp-darwin", "*darwin_all*/google-chat-mcp", "darwin universal binary"},
		{serverDir + "google-chat-mcp.exe", "*windows_amd64*/google-chat-mcp.exe", "windows amd64 binary"},
		{serverDir + "google-chat-mcp-linux-amd64", "*linux_amd64*/google-chat-mcp", "linux amd64 binary"},
		{serverDir + "google-chat-mcp-linux-arm64", "*linux_arm64*/google-chat-mcp", "linux arm64 binary"},
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
// the shipped manifest would come out alphabetized — unreviewable
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
		Type        string `json:"type"`
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
	userconfig.EnvDir:                                "a development override, which tests move the profile with",
	userconfig.EnvAllowOutsideHome:                   "goes with " + userconfig.EnvDir,
	credentials.EnvVar:                               "a credential for automation; a bundle install uses the login's keyring entry",
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

// toolsetsVar is the setting whose field has to list every toolset.
const toolsetsVar = config.EnvPrefix + "TOOLSETS"

// settingProblems holds what the bundle asks for to the server's own
// settings, and reports everything wrong rather than the first thing.
//
// Every setting bundleVarsFrom derives is asked for and reaches its
// variable, and nothing else is. A switch is asked as a boolean, so
// Claude Desktop shows it as one. The environment the defaults give the
// server configures it as no environment does: a bundle install may
// turn on nothing an unconfigured server leaves off, because in Claude
// Desktop the only other guard is its approval of each tool, which a
// person can set to always allow. A platform override carries no env,
// because the reference host replaces mcp_config.env with it rather
// than merging the two, and these checks read mcp_config.env alone.
// mcp_config.args is there and empty: the bundle passes every setting by
// env, an argument such as --enable-destructive=true would configure the
// server past these checks, and no Claude Desktop launch has shown the
// key optional. A platform override carries no args, an
// empty list included, since the host replaces mcp_config.args with it
// the same way. The toolsets field lists every toolset. And
// privacy_policies is not empty, which the manifest specification
// requires of a server that talks to a service processing user data,
// and which neither the schema nor `mcpb validate` enforces. leftOut is
// bundleLeftOut, or a test's.
func settingProblems(m bundleManifest, leftOut map[string]string) []string {
	settings := config.Vars()
	vars := map[string]config.Var{}
	for _, v := range settings {
		vars[v.Name] = v
	}
	bundleVars, problems := bundleVarsFrom(configVarsOf(settings), vars, leftOut)

	if len(m.PrivacyPolicies) == 0 {
		problems = append(problems, "privacy_policies is empty; the manifest specification requires one for a "+
			"server that connects to a service processing user data, as this one does to Google")
	}
	switch args := m.Server.MCPConfig.Args; {
	case args == nil:
		problems = append(problems, "mcp_config.args is missing; keep it as [], since no Claude Desktop launch "+
			"has shown the key optional")
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
	asked, toolsets := map[string]bool{}, config.ToolsetList()
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
		if vars[name].Switch && opt.Type != "boolean" {
			problems = append(problems, fmt.Sprintf("user_config.%s is a %s; %s is a switch, so ask for a boolean",
				key, cmp.Or(opt.Type, "field with no type"), name))
		}
		// The whole list, in order: a word check passes on "settings" or
		// "events" anywhere in the prose.
		if name == toolsetsVar && !strings.Contains(opt.Description, toolsets) {
			problems = append(problems, fmt.Sprintf("user_config.%s does not list the toolsets as %q", key, toolsets))
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
	if p := defaultsProblem(m); p != "" {
		problems = append(problems, p)
	}
	return problems
}

// defaultsProblem reports a bundle whose defaults configure the server
// otherwise than no environment does.
//
// The environment is built as the reference host builds it: a boolean
// becomes "true" or "false", a "" default stays "", and a reference
// with no default is passed through as written. A required setting
// with no default is left out, since the person supplies it and it is
// theirs either way. One with a default is not. The reference host
// starts the server only with the person's own value for it, but Claude
// Desktop's form may fill that in from the default, and a person may
// accept it unread; the evidence log has the source. A default of any
// other kind, a list or an object, is refused rather than guessed at.
func defaultsProblem(m bundleManifest) string {
	env := map[string]string{}
	for name, value := range m.Server.MCPConfig.Env {
		var b strings.Builder
		personal, last := false, 0
		for _, at := range userConfigRef.FindAllStringSubmatchIndex(value, -1) {
			opt := m.UserConfig[value[at[2]:at[3]]]
			sub := value[at[0]:at[1]]
			switch d := opt.Default.(type) {
			case bool:
				sub = strconv.FormatBool(d)
			case string:
				sub = d
			case float64:
				sub = strconv.FormatFloat(d, 'f', -1, 64)
			case nil:
				personal = personal || opt.Required
			default:
				return fmt.Sprintf("user_config.%s defaults to %s, which the defaults check cannot build an "+
					"environment from: give it a boolean, a string or a number", value[at[2]:at[3]], jsonKind(d))
			}
			b.WriteString(value[last:at[0]] + sub)
			last = at[1]
		}
		if !personal {
			env[name] = b.String() + value[last:]
		}
	}
	build := func(env map[string]string) (config.Config, error) {
		fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		return config.Define(fs, func(name string) string { return env[name] }).Build()
	}
	got, err := build(env)
	if err != nil {
		return fmt.Sprintf("the environment the bundle's defaults give the server is refused: %v", err)
	}
	want, err := build(nil)
	if err != nil {
		return fmt.Sprintf("an unconfigured server is refused: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Sprintf("the bundle's defaults configure the server otherwise than no environment does: %v", env)
	}
	return ""
}

// jsonKind names a decoded JSON value's kind, for a message.
func jsonKind(v any) string {
	switch v.(type) {
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("a %T", v)
}
