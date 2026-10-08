// Command gates runs this repository's own checks.
//
// It is a Go program rather than a shell script so that the code holding
// the gates shut is itself held to them: it compiles, it is vetted, it is
// linted, and it has tests. Three things a shell script is not, and each
// has cost something somewhere in this family of servers. `make check`
// also runs on Windows in CI, where bash is a dependency rather than a
// given, and a script that parses JSON with sed is how a quote ends up
// inside a string.
//
// goreleaser builds only ./cmd/..., so none of this ships.
//
//	go run ./scripts/gates classes
//	go run ./scripts/gates pins
//	go run ./scripts/gates api-coverage
//	go run ./scripts/gates api-diff
//	go run ./scripts/gates schema-diff [BINARY]
//	go run ./scripts/gates schema-baseline [BINARY]
//	go run ./scripts/gates smoke [BINARY]
//	go run ./scripts/gates staleness [BINARY]
//	go run ./scripts/gates tool-names FILE
//	go run ./scripts/gates config-vars
//	go run ./scripts/gates scopes
//	go run ./scripts/gates server-json VERSION CHECKSUMS
//	go run ./scripts/gates mcpb-manifest VERSION MANIFEST
//	go run ./scripts/gates mcpb-pack VERSION [DIST]
package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"

	"github.com/mmedum/google-chat-mcp/v5/internal/config"
	"github.com/mmedum/google-chat-mcp/v5/internal/credentials"
	"github.com/mmedum/google-chat-mcp/v5/internal/scopes"
	"github.com/mmedum/google-chat-mcp/v5/internal/userconfig"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// command is one thing this program does.
type command struct {
	// run receives the whole argument list, the command name included,
	// so an implementation reads its arguments at the offsets its arity
	// promises.
	run func(args []string, stdout, stderr io.Writer) int
	// arity is the fewest words the command takes, itself included, and
	// maxArity the most. A zero maxArity means exactly arity.
	arity    int
	maxArity int
	// args is how those words are spelled in the usage text.
	args string
	doc  string
	// runsIn says which pipeline has to run this command, and is what
	// the parity gate reads.
	runsIn where
}

// where is the pipeline a command belongs to.
//
// Three states rather than a bool, because there are three. A bool said
// "runs in `make check` and CI" or "does not", which put the release's
// own steps in the same bucket as a query a maintainer types by hand —
// so a release step could be dropped from the pipeline with nothing to
// notice. That is the failure the registry exists to prevent, one
// category over.
type where int

const (
	// manual is a developer convenience. Nothing has to run it.
	//
	// It starts at one so that the zero value is no pipeline at all: an
	// entry added without a runsIn would otherwise read as manual, and a
	// new gate wired into the Makefile and CI but not into the registry
	// would leave the parity gate green — which is the drift this field
	// exists to catch. TestEveryCommandDeclaresWhereItRuns refuses it.
	manual where = iota + 1
	// inCheck has to run in BOTH `make check` and the CI workflow.
	inCheck
	// inRelease has to run in the release pipeline: the goreleaser
	// config, or the release workflow.
	inRelease
)

// commands is the one list of what this program does.
//
// One list, because the dispatch, the usage text and the parity gate all
// read it. google-drive-mcp added a gate to two of those three and not
// the usage — in the commit whose whole purpose was to stop a list of
// gates drifting — so the only way to add a command here is to add it
// here.
//
// Four of these are asked by hand and by nothing else: tool-names,
// config-vars, scopes and mcpb-manifest. They were how the shell scripts
// asked Go a question, and the ports now call the same functions
// in-process, so no file in this repository invokes them. They are kept
// on purpose — each answers "what does the code actually say?" for a
// maintainer reading a doc or a manifest, which is the question this
// whole package exists to answer — and `runsIn: manual` is what says so.
//
// Filled in init rather than as a literal, because a command may read
// this map and Go sees that as an initialization cycle.
var commands map[string]command

func init() {
	commands = map[string]command{
		"schema-diff": {
			run: func(a []string, o, e io.Writer) int {
				return schemaDiffBinary(binaryArg(a), o, e)
			},
			arity: 1, maxArity: 2, args: "[BINARY]", runsIn: inCheck,
			doc: "the released tool surface, which a change may add to and never drop from",
		},
		"schema-baseline": {
			run: func(a []string, o, e io.Writer) int {
				return schemaBaseline(binaryArg(a), o, e)
			},
			arity: 1, maxArity: 2, args: "[BINARY]", runsIn: manual,
			doc: "record the surface of the release being cut, in its release commit",
		},
		"classes": {
			run:   classes,
			arity: 1, runsIn: inCheck,
			doc: "the tool error vocabulary, closed from both sides",
		},
		"transcript": {
			run: transcript, arity: 1,
			doc: "the drivers put nothing in their transcript unredacted", runsIn: inCheck,
		},
		"coverage": {
			run:   coverage,
			arity: 1, maxArity: 2, args: "[PROFILE]", runsIn: inCheck,
			doc: "statement coverage floor per package",
		},
		"smoke": {
			run:   smoke,
			arity: 1, maxArity: 2, args: "[BINARY]", runsIn: inCheck,
			doc: "the built server, driven over stdio with no credentials",
		},
		"staleness": {
			run:   staleness,
			arity: 1, maxArity: 2, args: "[BINARY]", runsIn: inCheck,
			doc: "the docs, held to what the code actually defines",
		},
		"tool-names": {
			run:   func(a []string, o, e io.Writer) int { return toolNames(a[1], o, e) },
			arity: 2, args: "FILE", runsIn: manual,
			doc: "the tool names in a schema dump, one per line",
		},
		"config-vars": {
			run: func(_ []string, o, _ io.Writer) int {
				_, _ = fmt.Fprintln(o, strings.Join(configVars(), "\n"))
				return 0
			},
			arity: 1, runsIn: manual,
			doc: "every GCM_ variable the server reads",
		},
		"pins": {
			run:   pins,
			arity: 1, runsIn: inCheck,
			doc: "every third-party tool held to an exact version",
		},
		"api-coverage": {
			run:   apiCoverage,
			arity: 1, runsIn: inCheck,
			doc: "every API method used on purpose or left out on purpose",
		},
		"api-fields": {
			run:   apiFields,
			arity: 1, runsIn: inCheck,
			doc: "every published field modeled on purpose or left out on purpose",
		},
		"api-diff": {
			run:   apiDiff,
			arity: 1, runsIn: manual,
			doc: "refetch the API method list and report what changed (needs the network)",
		},
		"release-notes": {
			run:   releaseNotes,
			arity: 2, maxArity: 3, args: "VERSION [CHANGELOG]", runsIn: inRelease,
			doc: "one version's CHANGELOG section, which is the release note",
		},
		"scopes": {
			run: func(_ []string, o, _ io.Writer) int {
				_, _ = fmt.Fprintln(o, strings.Join(scopes.All, "\n"))
				return 0
			},
			arity: 1, runsIn: manual,
			doc: "every OAuth scope this server asks for",
		},
		"server-json": {
			run:   func(a []string, o, e io.Writer) int { return serverJSON(a[1], a[2], o, e) },
			arity: 3, args: "VERSION CHECKSUMS", runsIn: inRelease,
			doc: "the MCP registry entry, from a release's own checksum file",
		},
		"mcpb-manifest": {
			run:   func(a []string, o, e io.Writer) int { return mcpbManifest(a[1], a[2], o, e) },
			arity: 3, args: "VERSION MANIFEST", runsIn: manual,
			doc: "the bundle manifest with a real version in it",
		},
		"mcpb-pack": {
			run:   mcpbPack,
			arity: 2, maxArity: 3, args: "VERSION [DIST]", runsIn: inRelease,
			doc: "the Claude Desktop bundle, from the binaries goreleaser built",
		},
	}
}

// commandsRunningIn are the commands one pipeline has to run, sorted.
func commandsRunningIn(w where) []string {
	out := make([]string, 0, len(commands))
	for name, c := range commands {
		if c.runsIn == w {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}
	c, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "gates: unknown check %q\n", args[0])
		usage(stderr)
		return 2
	}
	if len(args) < c.arity || len(args) > cmp.Or(c.maxArity, c.arity) {
		_, _ = fmt.Fprintf(stderr, "gates: %s takes %s\n", args[0], cmp.Or(c.args, "no arguments"))
		usage(stderr)
		return 2
	}
	return c.run(args, stdout, stderr)
}

// usage is generated from the command list, so it cannot fall behind it.
func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, "gates — this repository's own checks\n\nUsage:\n")
	names := slices.Sorted(maps.Keys(commands))
	width := 0
	for _, name := range names {
		if n := len(name) + len(commands[name].args) + 1; n > width {
			width = n
		}
	}
	for _, name := range names {
		spelled := strings.TrimSpace(name + " " + commands[name].args)
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", width, spelled, commands[name].doc)
	}
}

// dump is the shape of a --dump-schemas file, from either server. Only
// the fields the gate compares are declared.
type dump struct {
	Version string `json:"version"`
	Tools   []struct {
		Name string `json:"name"`
		// Decoded rather than kept raw: the comparison is by value, so
		// decoding once here beats decoding twice per comparison and
		// removes the branch that read a parse failure as "different".
		InputSchema  any `json:"inputSchema"`
		OutputSchema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"outputSchema"`
	} `json:"tools"`
}

func read(path string) (*dump, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDump(b, path)
}

// parseDump decodes a schema dump. source names where it came from, so a
// malformed one says which file or which binary produced it.
func parseDump(b []byte, source string) (*dump, error) {
	var d dump
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if len(d.Tools) == 0 {
		return nil, fmt.Errorf("%s registers no tools: a gate reading this would be looking at nothing", source)
	}
	return &d, nil
}

// runDumpSchemas asks the binary what it registers.
//
// Both gates that need the tool surface go through this, so neither can
// end up reading a file the other one wrote at a different time. That
// seam is what the shell wrappers had: one dumped the schemas and
// another compared them, and nothing said they were the same dump.
//
// It dumps the default surface. A GCM_ setting in the shell running the
// gate, such as read-only mode, changes what registers, and the profile
// in the real config directory is none of the dump's business.
func runDumpSchemas(bin string) ([]byte, error) {
	configDir, err := os.MkdirTemp("", "gates-dump-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(configDir) }()
	cmd := exec.Command(bin, "--dump-schemas")
	cmd.Env = isolatedEnv(configDir)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s --dump-schemas: %w", bin, err)
	}
	return out, nil
}

// schemaDiff compares the built binary's tool surface with a baseline.
//
// The baseline is the surface of the last release, and the promise is
// that a caller written against it keeps working: every tool comes back
// under the same name, with the same output fields. A reshaped input is
// reported rather than failed, because a schema can gain an optional
// argument without breaking anyone.
//
// want is the version the baseline has to be, from baselineVersion, or
// empty when there is none yet. A baseline from an older release fails,
// because it protects an older surface: whatever shipped since could be
// dropped and nothing would say.
func schemaDiff(baselinePath, currentPath, want string, stdout, stderr io.Writer) int {
	baseline, err := read(baselinePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	current, err := read(currentPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	type entry struct {
		input  any
		output map[string]json.RawMessage
	}
	index := func(d *dump) map[string]entry {
		m := make(map[string]entry, len(d.Tools))
		for _, t := range d.Tools {
			m[t.Name] = entry{input: t.InputSchema, output: t.OutputSchema.Properties}
		}
		return m
	}
	old, built := index(baseline), index(current)

	_, _ = fmt.Fprintf(stdout, "baseline %s: %d tools; built: %d tools\n", baseline.Version, len(old), len(built))

	var missing, extra, reshaped []string
	lost := map[string][]string{}
	for name := range old {
		if _, ok := built[name]; !ok {
			missing = append(missing, name)
			continue
		}
		var dropped []string
		for field := range old[name].output {
			if _, ok := built[name].output[field]; !ok {
				dropped = append(dropped, field)
			}
		}
		if len(dropped) > 0 {
			slices.Sort(dropped)
			lost[name] = dropped
		}
		// By value, not by bytes: a reordered key is the same schema,
		// and reporting every tool as reshaped would make the signal
		// worthless.
		if !reflect.DeepEqual(old[name].input, built[name].input) {
			reshaped = append(reshaped, name)
		}
	}
	for name := range built {
		if _, ok := old[name]; !ok {
			extra = append(extra, name)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	slices.Sort(reshaped)

	if len(reshaped) > 0 {
		_, _ = fmt.Fprintf(stdout, "\ninputs reshaped, look at these (%d): %s\n", len(reshaped), strings.Join(reshaped, ", "))
	}

	failed := false
	if want != "" && baseline.Version != want {
		failed = true
		_, _ = fmt.Fprintf(stdout, "\nFAIL: the baseline is the %s surface, but it must be %s's. "+
			"Refresh %s as CONTRIBUTING.md says.\n", baseline.Version, want, baselinePath)
	}
	// A tool in the baseline that is not in the build is one a caller
	// already depends on and can no longer call.
	if len(missing) > 0 {
		failed = true
		_, _ = fmt.Fprintf(stdout, "\nFAIL: %d tool(s) in the baseline are missing from the build:\n", len(missing))
		for _, name := range missing {
			_, _ = fmt.Fprintf(stdout, "  %s\n", name)
		}
	}
	// A new tool breaks nobody, so it is reported rather than failed. A
	// rename is not hidden by this: it takes a tool out of the baseline
	// too, and that half fails above.
	if len(extra) > 0 {
		_, _ = fmt.Fprintf(stdout, "\ntools added (%d): %s\n", len(extra), strings.Join(extra, ", "))
	}
	if len(lost) > 0 {
		failed = true
		_, _ = fmt.Fprintf(stdout, "\nFAIL: %d tool(s) lost an output field:\n", len(lost))
		for _, name := range slices.Sorted(maps.Keys(lost)) {
			_, _ = fmt.Fprintf(stdout, "  %s: %s\n", name, strings.Join(lost[name], ", "))
		}
	}
	if failed {
		return 1
	}
	return 0
}

// toolNames prints the tools in a dump, one per line, sorted. The
// staleness gate compares this with the README's tool table.
func toolNames(path string, stdout, stderr io.Writer) int {
	d, err := read(path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	_, _ = fmt.Fprintln(stdout, strings.Join(toolNamesIn(d), "\n"))
	return 0
}

// configVars is every GCM_ variable this server reads, sorted.
//
// It asks the code rather than grepping it. config.EnvVars reports what
// the settings actually look up, and the three variables defined outside
// that set are named by their own exported constants — so a renamed
// variable is a compile error here rather than a silently shorter list.
func configVars() []string {
	vars := append(config.EnvVars(),
		credentials.EnvVar, userconfig.EnvDir, userconfig.EnvAllowOutsideHome)
	slices.Sort(vars)
	return slices.Compact(vars)
}

// defaultBinary is where `make build` leaves the server, and what a
// command that takes an optional binary falls back to.
const defaultBinary = "./google-chat-mcp"

// binaryArg is the binary a command was pointed at, or the built one.
func binaryArg(args []string) string {
	return cmp.Or(argAt(args, 1), defaultBinary)
}

// baselinePath is the released surface a change may add to and never
// drop from.
const baselinePath = "testdata/schemas-baseline.json"

// dumpPath is where the built binary's surface is written. It is a build
// artifact and gitignored; the staleness gate reads it too.
const dumpPath = "schemas.json"

// schemaDiffBinary dumps the binary's tool surface and compares it with
// the baseline.
//
// This was a shell wrapper that dumped the schemas and called the
// comparison. It is one command now, because two halves in two languages
// is a seam with nothing holding it: the wrapper could point at a
// different file than the gate compared and nothing would say so.
func schemaDiffBinary(bin string, stdout, stderr io.Writer) int {
	out, err := runDumpSchemas(bin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	if err := os.WriteFile(dumpPath, out, 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: write %s: %v\n", dumpPath, err)
		return 1
	}
	raw, err := os.ReadFile(changelogPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	changelog := string(raw)
	want := baselineVersion(changelog)
	if _, err := os.Stat(baselinePath); err != nil {
		if want != "" {
			_, _ = fmt.Fprintf(stdout, "FAIL: %s names %s as released, but %s is missing.\n", changelogPath, want, baselinePath)
			return 1
		}
		// The first release is exactly this state, and it is not a
		// failure: there is nothing yet to be compatible with.
		_, _ = fmt.Fprintf(stdout, "no baseline at %s; wrote %s\n", baselinePath, dumpPath)
		return 0
	}
	if code := schemaDiff(baselinePath, dumpPath, want, stdout, stderr); code != 0 {
		return code
	}
	// With nothing unreleased, this build is the newest release itself, so
	// its surface is the baseline's exactly. That holds in the release
	// commit, where it proves the baseline was recorded rather than
	// relabeled, and it holds until the next change, which the staleness
	// gate makes put something under [Unreleased].
	if want == "" || sectionFor(changelog, "Unreleased") != "" {
		return 0
	}
	same, err := sameTools(baselinePath, dumpPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if !same {
		_, _ = fmt.Fprintf(stdout, "\nFAIL: nothing is under [Unreleased], so this build is %s, and its tools differ from the baseline. "+
			"In %s's release commit, run `make schema-baseline VERSION=%s`. Otherwise, say what changed under [Unreleased].\n",
			want, want, want)
		return 1
	}
	return 0
}

// sameTools reports whether two schema dumps register the same tools,
// field for field. The version and SDK stamps are not part of it.
func sameTools(aPath, bPath string) (bool, error) {
	var tools [2]struct {
		Tools any `json:"tools"`
	}
	for i, path := range []string{aPath, bPath} {
		b, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(b, &tools[i]); err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	}
	return reflect.DeepEqual(tools[0].Tools, tools[1].Tools), nil
}

// schemaBaseline records the surface of the release being cut as the
// baseline. The release commit runs it, so the baseline lands with the
// changelog heading that names it.
//
// It checks the build against the current baseline first, and refuses
// when the build drops a tool or an output field. Overwriting first would
// leave the diff comparing the release with itself.
func schemaBaseline(bin string, stdout, stderr io.Writer) int {
	out, err := runDumpSchemas(bin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	if err := os.WriteFile(dumpPath, out, 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: write %s: %v\n", dumpPath, err)
		return 1
	}
	built, err := parseDump(out, bin)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	changelog, err := os.ReadFile(changelogPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	want := baselineVersion(string(changelog))
	if want == "" {
		_, _ = fmt.Fprintf(stdout, "FAIL: %s names no release yet, so there is no surface to record.\n", changelogPath)
		return 1
	}
	if built.Version != want {
		_, _ = fmt.Fprintf(stdout, "FAIL: %s is stamped %s, but the release being cut is %s. "+
			"Build it with `make build VERSION=%s`.\n", bin, built.Version, want, want)
		return 1
	}
	if previous, err := read(baselinePath); err == nil {
		if code := schemaDiff(baselinePath, dumpPath, "", stdout, stderr); code != 0 {
			_, _ = fmt.Fprintf(stdout, "\n%s is unchanged.\n", baselinePath)
			if previous.Version == want {
				_, _ = fmt.Fprintf(stdout, "It already holds %s from an earlier run, so a tool new in %s counts as released. "+
					"Restore the last release's baseline from its tag, then run this again.\n", want, want)
			}
			return code
		}
	}
	if err := writeThrough(baselinePath, func(w io.Writer) error {
		_, err := w.Write(out)
		return err
	}); err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: write %s: %v\n", baselinePath, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "%s is now the %s surface: %d tools\n", baselinePath, want, len(built.Tools))
	return 0
}

// writeThrough writes path through a temporary file beside it and a
// rename, so a failed write leaves whatever was there whole rather than
// truncated or half-written.
func writeThrough(path string, write func(io.Writer) error) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // a path this program names, never one from input
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := write(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// baselineVersion is the release whose surface the baseline must hold:
// the changelog's newest heading, or empty before the first release.
//
// Between releases that heading is the last tag. In a release commit it
// is the release being cut, and the baseline is refreshed in that same
// commit. Refreshing after the tag instead would fail every branch from
// the moment the tag is pushed until a second change lands.
func baselineVersion(changelog string) string {
	if v := newestVersion(changelog); v != "" {
		return "v" + v
	}
	return ""
}

// argAt is args[i] when there is one, so a command with an optional
// argument reads it without bounds-checking at every call site.
func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}
