// Command devcheck is the JSON half of the repository's gates. The
// shell scripts do the git and file plumbing; anything that reads a tool
// schema or knows what the code defines happens here, in the language
// the project is written in.
//
// It exists so the gates need no interpreter beyond the one this
// repository is already written in: an undeclared prerequisite is one
// every contributor and CI runner has to discover for themselves.
//
//	go run ./internal/devcheck schema-diff BASELINE CURRENT
//	go run ./internal/devcheck tool-names FILE
//	go run ./internal/devcheck config-vars
//	go run ./internal/devcheck scopes
//	go run ./internal/devcheck server-json VERSION CHECKSUMS
//	go run ./internal/devcheck mcpb-manifest VERSION MANIFEST
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/credentials"
	"github.com/mmedum/google-chat-mcp/internal/scopes"
	"github.com/mmedum/google-chat-mcp/internal/userconfig"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

const usage = "usage: devcheck schema-diff BASELINE CURRENT | tool-names FILE | config-vars | scopes | " +
	"server-json VERSION CHECKSUMS | mcpb-manifest VERSION MANIFEST"

// arity is how many words each subcommand takes, itself included. An
// unknown command is absent here and so matches nothing, which is why
// the dispatch below needs no default.
var arity = map[string]int{
	"schema-diff":   3,
	"tool-names":    2,
	"config-vars":   1,
	"scopes":        1,
	"server-json":   3,
	"mcpb-manifest": 3,
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || len(args) != arity[args[0]] {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "schema-diff":
		return schemaDiff(args[1], args[2], stdout, stderr)
	case "tool-names":
		return toolNames(args[1], stdout, stderr)
	case "config-vars":
		_, _ = fmt.Fprintln(stdout, strings.Join(configVars(), "\n"))
	case "scopes":
		_, _ = fmt.Fprintln(stdout, strings.Join(scopes.All, "\n"))
	case "server-json":
		return serverJSON(args[1], args[2], stdout, stderr)
	case "mcpb-manifest":
		return mcpbManifest(args[1], args[2], stdout, stderr)
	}
	return 0
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
