package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadmeToolNamesReadsTheTableAndNothingElse(t *testing.T) {
	const readme = "# google-chat-mcp\n" +
		"\n" +
		"Call `send_message` to post. That sentence is prose, not a row.\n" +
		"\n" +
		"| Tool | What it does | Scope |\n" +
		"|---|---|---|\n" +
		"| `whoami` | Which account is signed in | `openid` |\n" +
		"| `list_spaces` | The spaces you are in | `chat.spaces.readonly` |\n" +
		"| `get_space` | One space by resource name | `chat.spaces.readonly` |\n" +
		"\n" +
		"| Setting | Default |\n" +
		"|---|---|\n" +
		"| `GCM_PROFILE` | default |\n"

	got := readmeToolNames(readme)
	want := []string{"get_space", "list_spaces", "whoami"}
	if !slices.Equal(got, want) {
		t.Errorf("readmeToolNames\n got %v\nwant %v", got, want)
	}
	// The settings table's row is upper case, so it is not a tool name.
	// If that ever stops being true the gate reports a tool the server
	// does not register, which is the safe direction.
	if slices.Contains(got, "GCM_PROFILE") {
		t.Error("readmeToolNames took a row out of the settings table")
	}
}

// A rule under another server's name is not this server's to check,
// and a tool named in prose is not a rule.
func TestPermissionRuleToolsReadsOnlyThisServersRules(t *testing.T) {
	const readme = "Call `mcp__google-chat__whoami` first.\n" +
		"```json\n" +
		`"ask": ["mcp__google-chat__send_message", "mcp__google-chat__delete_mesage",` + "\n" +
		`  "mcp__gitlab__merge_merge_request"]` + "\n" +
		"```\n"
	got := permissionRuleTools(readme)
	want := []string{"send_message", "delete_mesage"}
	if !slices.Equal(got, want) {
		t.Errorf("permissionRuleTools\n got %v\nwant %v", got, want)
	}
}

// One scope is a prefix of another, and the shorter one is usually the
// write scope: a page listing only chat.spaces.readonly would read as
// documenting chat.spaces too, and that is the scope whose absence from
// the consent screen turns every write into a 403.
func TestEndsALineDoesNotAcceptAPrefix(t *testing.T) {
	doc := strings.Split("Paste these in:\n\n"+
		"https://www.googleapis.com/auth/chat.spaces.readonly\n"+
		"https://www.googleapis.com/auth/chat.messages\n"+
		"openid\n", "\n")

	tests := []struct {
		scope string
		want  bool
	}{
		{"https://www.googleapis.com/auth/chat.spaces.readonly", true},
		{"https://www.googleapis.com/auth/chat.messages", true},
		{"openid", true},
		{"https://www.googleapis.com/auth/chat.spaces", false},
		{"https://www.googleapis.com/auth/chat.messages.readonly", false},
		{"email", false},
	}
	for _, tt := range tests {
		if got := endsALine(doc, tt.scope); got != tt.want {
			t.Errorf("endsALine(%q) = %v, want %v", tt.scope, got, tt.want)
		}
	}
	// A trailing space is invisible in a diff and must not decide this.
	if !endsALine([]string{"openid  "}, "openid") {
		t.Error("a trailing space hid a scope that is there")
	}
}

const changelogUnreleased = `# Changelog

## [Unreleased]

### Fixed

- A deleted message reads back as a tombstone.

## [1.0.0] - 2026-09-05

### Added

- The first release.

[Unreleased]: https://example.com/compare/v1.0.0...HEAD
`

const changelogEmptyUnreleased = `# Changelog

## [Unreleased]

## [1.0.0] - 2026-09-05

### Added

- The first release.
`

const changelogReleasing = `# Changelog

## [Unreleased]

## [1.1.0] - 2026-09-06

### Added

- The release being cut.

## [1.0.0] - 2026-09-05

### Added

- The first release.
`

func TestNewestVersion(t *testing.T) {
	for _, tt := range []struct{ changelog, want string }{
		{changelogUnreleased, "1.0.0"},
		{changelogReleasing, "1.1.0"},
		{"# Changelog\n\n## [Unreleased]\n\n- The first version is not cut yet.\n", ""},
	} {
		if got := newestVersion(tt.changelog); got != tt.want {
			t.Errorf("newestVersion = %q, want %q", got, tt.want)
		}
	}
}

func TestChangelogProblem(t *testing.T) {
	tests := []struct {
		name      string
		changelog string
		tag       string
		// want is a fragment of the problem; empty means no problem.
		want string
	}{
		{
			name:      "content under Unreleased is what this usually wants",
			changelog: changelogUnreleased, tag: "v1.0.0",
		},
		{
			// The release commit moves the content under a version
			// heading, and CI runs on the release pull request before
			// the tag exists. Failing here would block every release,
			// which is how google-docs-mcp found this.
			name:      "a heading newer than the tag is the release being cut",
			changelog: changelogReleasing, tag: "v1.0.0",
		},
		{
			name:      "nothing new at all",
			changelog: changelogEmptyUnreleased, tag: "v1.0.0",
			want: "documents nothing new",
		},
		{
			name:      "the tag is compared without its v",
			changelog: changelogEmptyUnreleased, tag: "1.0.0",
			want: "documents nothing new",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := changelogProblem(tt.changelog, tt.tag)
			switch {
			case tt.want == "" && got != "":
				t.Errorf("expected no problem, got %q", got)
			case tt.want != "" && !strings.Contains(got, tt.want):
				t.Errorf("problem\n got %q\nwant something containing %q", got, tt.want)
			}
		})
	}
}

// The configuration page names every variable the server reads, and
// says what a profile name is in the server's own words.
func TestConfigDocProblems(t *testing.T) {
	doc := readRepoFile(t, configDocPath)
	if got := configDocProblems(doc); len(got) != 0 {
		t.Errorf("the committed page: %q", got)
	}
	for _, tc := range []struct{ name, old, new, want string }{
		{"a variable left out", "GCM_LOCAL_DIR", "GCM_LOCALDIR", "docs/configuration.md does not mention GCM_LOCAL_DIR"},
		{"a name kept only inside a longer one", "`GCM_CONFIG_DIR`", "`GCM_CONFIG_DIRECTORY`",
			"docs/configuration.md does not mention GCM_CONFIG_DIR"},
		{"the profile rule in other words", "- and _, starting", "`-` and `_`, starting",
			`docs/configuration.md does not say a profile name is "up to 64 characters from a to z, 0 to 9, - and _, ` +
				`starting with a letter or digit"`},
	} {
		if !strings.Contains(doc, tc.old) {
			t.Fatalf("%s: the page does not say %q", tc.name, tc.old)
		}
		if got := configDocProblems(strings.ReplaceAll(doc, tc.old, tc.new)); !slices.Equal(got, []string{tc.want}) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The CHANGELOG rule measures what ships: a change confined to tests or
// their fixtures needs no entry, and a change to anything else under cmd/
// or internal/ does.
func TestSourceChangedSinceSkipsTestsAndFixtures(t *testing.T) {
	tracked := []string{
		"internal/tools/tools.go",
		"internal/tools/tools_test.go",
		"internal/tools/testdata/emoji.png",
		"internal/tools/testdata/nested/page.json",
		"internal/testdatax/x.go",
		"cmd/server/main.go",
		"docs/guide.md",
	}
	for _, tc := range []struct {
		name, edit string
		want       bool
	}{
		{"nothing edited", "", false},
		{"a fixture", "internal/tools/testdata/emoji.png", false},
		{"a fixture nested in testdata", "internal/tools/testdata/nested/page.json", false},
		{"a test", "internal/tools/tools_test.go", false},
		{"a doc outside cmd and internal", "docs/guide.md", false},
		{"a source file", "internal/tools/tools.go", true},
		{"a binary's source", "cmd/server/main.go", true},
		{"a package whose name only starts with testdata", "internal/testdatax/x.go", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			initRepo(t)
			for _, rel := range tracked {
				put(t, dir, rel, "v1\n")
			}
			gitRun(t, "add", "-A")
			gitRun(t, "commit", "--quiet", "-m", "v1")
			gitRun(t, "tag", "v1.0.0")
			if tc.edit != "" {
				put(t, dir, tc.edit, "v2\n")
			}
			got, err := sourceChangedSince("v1.0.0")
			if err != nil || got != tc.want {
				t.Errorf("sourceChangedSince after editing %q = %v, %v; want %v, nil", tc.edit, got, err, tc.want)
			}
		})
	}
}

// Outside a git checkout, with no binary and no docs, no rule reaches a
// verdict: the gate says so and exits 2. A real finding beside that is
// still a finding, and exits 1.
func TestTheStalenessGateNeedsAGitCheckout(t *testing.T) {
	const noGit = "git cannot read this tree, so the CHANGELOG half of this gate cannot run: exit status 128"
	for _, tc := range []struct {
		name     string
		setupDoc string
		wantCode int
		want     []string
	}{
		{name: "nothing to judge", wantCode: 2, want: []string{noGit}},
		{name: "a setup page that lists no scope", setupDoc: "# Setup\n", wantCode: 1,
			want: []string{"docs/gcp-setup.md does not list the scope openid", noGit}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
			if tc.setupDoc != "" {
				put(t, dir, setupDocPath, tc.setupDoc)
			}
			var out, errOut bytes.Buffer
			code := run([]string{"staleness", filepath.Join(dir, "no-binary")}, &out, &errOut)
			problems := gatesProblems(t, errOut.String())
			if code != tc.wantCode || out.Len() != 0 || slices.ContainsFunc(tc.want, func(w string) bool { return !slices.Contains(problems, w) }) {
				t.Errorf("exit %d, stdout %q, problems %q; want %d, nothing and %q among them",
					code, out.String(), problems, tc.wantCode, tc.want)
			}
		})
	}
}

// embedsFromTestdata is every //go:embed line under root's cmd/ and
// internal/ outside a test that names a testdata path, and how many Go
// files it read.
func embedsFromTestdata(t *testing.T, root string) (read int, found []string) {
	t.Helper()
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(p) //nolint:gosec // a path the walk found
			if err != nil {
				return err
			}
			read++
			for i, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "//go:embed") && strings.Contains(line, "testdata") {
					found = append(found, fmt.Sprintf("%s:%d", p, i+1))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return read, found
}

// The CHANGELOG rule skips testdata on the grounds that none of it ships.
// A //go:embed outside a test would ship a fixture, and the rule would
// then miss a change to it.
func TestNoShippedCodeEmbedsATestdataFile(t *testing.T) {
	read, found := embedsFromTestdata(t, repoRoot(t))
	if read < 50 {
		t.Fatalf("read %d Go files under cmd/ and internal/, fewer than 50: this test is looking at nothing", read)
	}
	for _, at := range found {
		t.Errorf("%s embeds a testdata file, which then ships; move it out of testdata", at)
	}

	fixture := t.TempDir()
	put(t, fixture, "cmd/server/main.go", "package main\n\n// go:embed testdata/x is prose, not a directive.\nfunc main() {}\n")
	put(t, fixture, "internal/tools/assets.go", "package tools\n\nimport _ \"embed\"\n\n//go:embed testdata/emoji.png\nvar emoji []byte\n")
	put(t, fixture, "internal/tools/assets_test.go", "package tools\n\n//go:embed testdata/emoji.png\nvar fixture []byte\n")
	if _, found := embedsFromTestdata(t, fixture); !slices.Equal(found, []string{filepath.Join(fixture, "internal/tools/assets.go") + ":5"}) {
		t.Errorf("an embed of a fixture outside a test: found %q, want the one line", found)
	}
}
