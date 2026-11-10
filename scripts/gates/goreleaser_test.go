package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The reader records every value by its key path, the index of each
// sequence item on the way and its line: an item's first key on its dash,
// a sequence at its key's own column, a one-line list of words, a block
// scalar from its first line, and a value with a comment after it.
func TestGoreleaserReadsEveryValueByPath(t *testing.T) {
	g := parseGoreleaser(textLines(
		"version: 2",
		"builds:",
		"  - id: server # the one build",
		"    goos: [linux, darwin]",
		"    hooks:",
		"      post:",
		"      - go run ./scripts/gates mcpb-pack {{ .Version }} dist",
		"      - |",
		"        go mod tidy",
		"  - main: ./cmd/x",
		"    ldflags:",
		"      - -s -w",
		"checksum:",
		"  name_template: 'checksums.txt'",
		"release:",
		"  footer: >-",
		"    one",
		"    two",
	))
	if len(g.refused) > 0 {
		t.Fatalf("refused %q", g.refused)
	}
	got := make([]string, 0, len(g.values))
	for _, v := range g.values {
		got = append(got, fmt.Sprintf("%d %s%v %q", v.line, v.keys, v.items, v.value))
	}
	want := []string{
		`1 version[] "2"`,
		`3 builds.id[0] "server"`,
		`4 builds.goos[0 0] "linux"`,
		`4 builds.goos[0 1] "darwin"`,
		`7 builds.hooks.post[0 0] "go run ./scripts/gates mcpb-pack {{ .Version }} dist"`,
		`9 builds.hooks.post[0 1] "go mod tidy"`,
		`10 builds.main[1] "./cmd/x"`,
		`12 builds.ldflags[1 0] "-s -w"`,
		`14 checksum.name_template[] "checksums.txt"`,
		`17 release.footer[] "one two"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("values:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	// A hook is a command line, its template actions rendered; a release
	// footer is prose.
	runs := make([]string, 0, len(g.runs))
	for _, r := range g.runs {
		runs = append(runs, fmt.Sprintf("%d %s", r.line, r.text))
	}
	if want := []string{"7 go run ./scripts/gates mcpb-pack {{}} dist", "9 go mod tidy"}; !slices.Equal(runs, want) {
		t.Errorf("runs = %q, want %q", runs, want)
	}
}

// A command keeps its words whole: an action that is a word of its own is
// one word, one inside a word renders nothing, and a wrapper's actions
// and a comment disappear.
func TestGoreleaserCommandKeepsWordsWhole(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"go run ./scripts/gates mcpb-pack dist {{ .Version }} dist/google-docs-mcp_{{ .Version }}.mcpb",
			"go run ./scripts/gates mcpb-pack dist {{}} dist/google-docs-mcp_.mcpb"},
		{"{{ if not .IsSnapshot }}go run ./scripts/gates mcpb-pack dist {{ .Version }} out.mcpb{{ end }}",
			"go run ./scripts/gates mcpb-pack dist {{}} out.mcpb"},
		{"{{- if .IsSnapshot -}} echo a {{- else -}} echo b {{- end -}}", "echo a  echo b"},
		{"{{/* a comment */}} go mod tidy", "go mod tidy"},
		{"echo {{ .Os }}{{ .Arch }} done", "echo {{}} done"},
		{`echo "{{ .Version }}" -X=main.v={{ .Version }} {{ .Os }}.zip`, `echo "" -X=main.v= .zip`},
		{"{{ range .Env }} x{{ end }} {{ with .Tag }} y{{ end }}", "x  y"},
		{"{{ range .Env }}{{ if .Skip }}{{ continue }}{{ end }} x {{ break }}{{ end }}", "x"},
	} {
		if got := goreleaserCommand(tc.command); got != tc.want {
			t.Errorf("goreleaserCommand(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

// A shape the reader would misread is refused by name, anywhere in the
// config, and what sits under a key it cannot read is not read. A command
// a control action splits is refused; one wrapped whole in one if or with
// is read.
func TestGoreleaserRefusesWhatItCannotRead(t *testing.T) {
	g := parseGoreleaser(textLines(
		"'archives':",
		"  - formats: [zip]",
		"builds:",
		"  - goos: &oses [linux]",
		"    goarch: {a: b}",
		"    env:",
		"      - - CGO_ENABLED=0",
		"    flags: [\"-trimpath\"]",
		"    main: \"./cmd/\\x\"",
		"    binary: x",
		"      y",
		"before:",
		"  hooks:",
		`    - "{{ if .IsSnapshot }}true{{ else }}go run github.com/someone/tool@main dist{{ end }}"`,
		`    - "{{ if .IsSnapshot }}true{{ end }}{{ if not .IsSnapshot }}go run github.com/someone/tool@main dist{{ end }}"`,
		`    - "{{ range .Env }}go run github.com/someone/tool@main dist{{ end }}"`,
		"    - echo {{ if .IsSnapshot }}go run github.com/someone/tool@main dist{{ end }}",
		`    - "{{ if .IsSnapshot }}go run github.com/someone/tool@main{{ end }} dist"`,
		`    - "{{ if not .Prerelease }}go run ./scripts/gates mcpb-pack dist{{ end }}"`,
		`    - "{{- with .Env.TIDY -}} go mod tidy {{- end -}}"`,
	))
	const branches = ": a template control action; goreleaser runs one branch, and the gates read only " +
		"a command wrapped whole in one {{ if }}...{{ end }}"
	want := []string{
		".goreleaser.yaml:1: 'archives': a quoted key; write the key plain",
		".goreleaser.yaml:4: goos: an anchor, an alias or a tag; write the value out",
		".goreleaser.yaml:5: goarch: a flow collection; write it as a block, one key or item a line",
		".goreleaser.yaml:7: env: an empty item or an item holding a sequence; write the item's value on its dash",
		".goreleaser.yaml:8: flags: a flow collection; write it as a block, one key or item a line",
		".goreleaser.yaml:9: main: a quoted value with an escape in it; write it unquoted or as a block",
		".goreleaser.yaml:11: binary: a value continued on the next line; write it on one line or as a block scalar",
		".goreleaser.yaml:14: hooks" + branches,
		".goreleaser.yaml:15: hooks" + branches,
		".goreleaser.yaml:16: hooks" + branches,
		".goreleaser.yaml:17: hooks" + branches,
		".goreleaser.yaml:18: hooks" + branches,
	}
	if !slices.Equal(g.refused, want) {
		t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(g.refused, "\n  "), strings.Join(want, "\n  "))
	}
	if slices.ContainsFunc(g.values, func(v goreleaserValue) bool {
		return v.keys == "archives" || strings.HasPrefix(v.keys, "archives.")
	}) {
		t.Errorf("read a value under a quoted key: %v", g.values)
	}
	var runs []string
	for _, r := range g.runs {
		runs = append(runs, fmt.Sprintf("%d %s", r.line, r.text))
	}
	if want := []string{"19 go run ./scripts/gates mcpb-pack dist", "20 go mod tidy"}; !slices.Equal(runs, want) {
		t.Errorf("runs = %q, want %q", runs, want)
	}
	if got, want := parseGoreleaser("- top\n").refused, []string{".goreleaser.yaml:1: an item outside any key; the config is a mapping"}; !slices.Equal(got, want) {
		t.Errorf("a sequence at the top: %q, want %q", got, want)
	}
}
