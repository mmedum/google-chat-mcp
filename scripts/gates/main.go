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
//	go run ./scripts/gates schema-diff BASELINE CURRENT
//	go run ./scripts/gates tool-names FILE
//	go run ./scripts/gates config-vars
//	go run ./scripts/gates scopes
//	go run ./scripts/gates server-json VERSION CHECKSUMS
//	go run ./scripts/gates mcpb-manifest VERSION MANIFEST
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

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/credentials"
	"github.com/mmedum/google-chat-mcp/internal/scopes"
	"github.com/mmedum/google-chat-mcp/internal/userconfig"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// command is one thing this program does.
type command struct {
	// run receives the whole argument list, the command name included,
	// so an implementation reads its arguments at the offsets its arity
	// promises.
	run func(args []string, stdout, stderr io.Writer) int
	// arity is how many words the command takes, itself included.
	arity int
	// args is how those words are spelled in the usage text.
	args string
	doc  string
	// gate marks a command that has to run in BOTH `make check` and the
	// CI workflow. Anything else is a developer convenience.
	gate bool
}

// commands is the one list of what this program does.
//
// One list, because the dispatch, the usage text and the parity gate all
// read it. google-drive-mcp added a gate to two of those three and not
// the usage — in the commit whose whole purpose was to stop a list of
// gates drifting — so the only way to add a command here is to add it
// here.
//
// Filled in init rather than as a literal, because a command may read
// this map and Go sees that as an initialisation cycle.
var commands map[string]command

func init() {
	commands = map[string]command{
		"schema-diff": {
			run:   func(a []string, o, e io.Writer) int { return schemaDiffBinary(a[1], o, e) },
			arity: 2, args: "[BINARY]", gate: true,
			doc: "the released tool surface, which a change may add to and never drop from",
		},
		"coverage": {
			run:   coverage,
			arity: 2, args: "[PROFILE]", gate: true,
			doc: "statement coverage floor per package",
		},
		"tool-names": {
			run:   func(a []string, o, e io.Writer) int { return toolNames(a[1], o, e) },
			arity: 2, args: "FILE",
			doc: "the tool names in a schema dump, one per line",
		},
		"config-vars": {
			run: func(_ []string, o, _ io.Writer) int {
				_, _ = fmt.Fprintln(o, strings.Join(configVars(), "\n"))
				return 0
			},
			arity: 1,
			doc:   "every GCM_ variable the server reads",
		},
		"scopes": {
			run: func(_ []string, o, _ io.Writer) int {
				_, _ = fmt.Fprintln(o, strings.Join(scopes.All, "\n"))
				return 0
			},
			arity: 1,
			doc:   "every OAuth scope this server asks for",
		},
		"server-json": {
			run:   func(a []string, o, e io.Writer) int { return serverJSON(a[1], a[2], o, e) },
			arity: 3, args: "VERSION CHECKSUMS",
			doc: "the MCP registry entry, from a release's own checksum file",
		},
		"mcpb-manifest": {
			run:   func(a []string, o, e io.Writer) int { return mcpbManifest(a[1], a[2], o, e) },
			arity: 3, args: "VERSION MANIFEST",
			doc: "the bundle manifest with a real version in it",
		},
	}
}

// gateNames are the commands that must run in both places, sorted.
func gateNames() []string {
	out := make([]string, 0, len(commands))
	for name, c := range commands {
		if c.gate {
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
	if len(args) != c.arity {
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
	var d dump
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &d, nil
}

// schemaDiff compares the built binary's tool surface with a baseline.
//
// The baseline is the surface of the last release, and the promise is
// that a caller written against it keeps working: every tool comes back
// under the same name, with the same output fields. A reshaped input is
// reported rather than failed, because a schema can gain an optional
// argument without breaking anyone.
func schemaDiff(baselinePath, currentPath string, stdout, stderr io.Writer) int {
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
	names := make([]string, 0, len(d.Tools))
	for _, t := range d.Tools {
		names = append(names, t.Name)
	}
	slices.Sort(names)
	_, _ = fmt.Fprintln(stdout, strings.Join(names, "\n"))
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

// baselinePath is the released surface a change may add to and never
// drop from.
const baselinePath = "testdata/schemas-baseline.json"

// dumpPath is where the built binary's surface is written. It is a build
// artefact and gitignored; the staleness gate reads it too.
const dumpPath = "schemas.json"

// schemaDiffBinary dumps the binary's tool surface and compares it with
// the baseline.
//
// This was a shell wrapper that dumped the schemas and called the
// comparison. It is one command now, because two halves in two languages
// is a seam with nothing holding it: the wrapper could point at a
// different file than the gate compared and nothing would say so.
func schemaDiffBinary(bin string, stdout, stderr io.Writer) int {
	out, err := exec.Command(bin, "--dump-schemas").Output()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %s --dump-schemas: %v\n", bin, err)
		return 1
	}
	if err := os.WriteFile(dumpPath, out, 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: write %s: %v\n", dumpPath, err)
		return 1
	}
	if _, err := os.Stat(baselinePath); err != nil {
		// The first release is exactly this state, and it is not a
		// failure: there is nothing yet to be compatible with.
		_, _ = fmt.Fprintf(stdout, "no baseline at %s; wrote %s\n", baselinePath, dumpPath)
		return 0
	}
	return schemaDiff(baselinePath, dumpPath, stdout, stderr)
}
