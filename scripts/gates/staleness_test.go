package main

import (
	"bytes"
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
		// A prerelease is a release (https://semver.org/#spec-item-9), and a
		// heading in a fence is an example.
		{"## [Unreleased]\n\n## [2.0.0-rc.1] - 2026-10-01\n\n- c\n\n## [1.0.0]\n", "2.0.0-rc.1"},
		{"## [Unreleased]\n\n```\n## [9.9.9]\n```\n\n## [1.0.0]\n", "1.0.0"},
		{"## [Unreleased]\n\n## [1.0.0--]\n\n## [1.0.0]\n", "1.0.0"},
	} {
		if got := newestVersion(tt.changelog); got != tt.want {
			t.Errorf("newestVersion = %q, want %q", got, tt.want)
		}
	}
}

// The changelog fixtures: a released v0.9.0, a v0.9.1 with and without
// notes, and an [Unreleased] section with and without them. This
// repository never had a 0.9 tag, so a git call that reads the wrong
// checkout finds neither.
const (
	clReleased   = "## [0.9.0] - 2026-01-01\n\n### Added\n\n- The first tool.\n"
	clNext       = "## [0.9.1] - 2026-02-01\n\n### Added\n\n- A second tool.\n\n"
	clNextEmpty  = "## [0.9.1] - 2026-02-01\n\n"
	clUnreleased = "# Changelog\n\n## [Unreleased]\n\n"
	clWritten    = clUnreleased + "### Added\n\n- A second tool.\n\n"
	clEmpty      = "the binary's source changed but CHANGELOG.md [Unreleased] is empty"
)

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

// stalenessRepo is a git checkout of a module whose binary, cmd/fixture,
// imports internal/a, which embeds a file, and not internal/unused, with a
// release tag v0.9.0 on its one commit. A case changes a stalenessCopy of
// it.
func stalenessRepo(t *testing.T) string {
	t.Helper()
	isolateGit(t)
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	for name, body := range map[string]string{
		changelogPath: clUnreleased + clReleased,
		"go.mod": textLines("module example.com/fixture", "", "go 1.21", "",
			"require example.com/dep v1.0.0", "", "replace example.com/dep => ./dep"),
		"dep/go.mod": textLines("module example.com/dep", "", "go 1.21"),
		"dep/dep.go": textLines("package dep"),
		"cmd/fixture/main.go": textLines("package main", "", `import (`, `	_ "example.com/dep"`,
			`	_ "example.com/fixture/internal/a"`, ")", "", "func main() {}"),
		"tools.go":              textLines("package fixture"),
		"internal/a/a.go":       textLines("package a", "", `import _ "embed"`, "", "//go:embed data/x.txt", "var X string"),
		"internal/a/b.go":       textLines("package a"),
		"internal/a/data/x.txt": "x\n",
		"internal/a/notes.md":   "notes\n",
		"internal/unused/u.go":  textLines("package unused"),
		"internal/a/a_test.go":  textLines("package a"),
		"internal/a/testdata/x": "x\n",
		"scripts/gates/main.go": textLines("package main", "", "func main() {}"),
		"docs/x.md":             "x\n",
	} {
		put(t, dir, name, body)
	}
	stalenessGit(t, dir, "init", "-q", "-b", "main")
	stalenessGit(t, dir, "add", "-A")
	stalenessGit(t, dir, "commit", "-q", "-m", "first")
	stalenessGit(t, dir, "tag", "v0.9.0")
	return dir
}

// stalenessCopy is a copy of the checkout at dir, for a case to change.
func stalenessCopy(t *testing.T, dir string) string {
	t.Helper()
	cp := t.TempDir()
	if err := os.CopyFS(cp, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	return cp
}

// stalenessGit runs git in the checkout at dir, failing the test on an
// error.
func stalenessGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitRun(t, append([]string{"-C", dir}, args...)...)
}

// stalenessWrite writes the files with their new contents, a file with no
// contents removed.
func stalenessWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if body == "" {
			stalenessGit(t, dir, "rm", "-q", name)
		} else {
			put(t, dir, name, body)
		}
	}
}

// stalenessChange writes the files as stalenessWrite does and commits them.
func stalenessChange(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	stalenessWrite(t, dir, files)
	stalenessGit(t, dir, "add", "-A")
	stalenessGit(t, dir, "commit", "-q", "-m", "change")
}

// A committed change to what users get needs an entry: any file but a test
// in a package the binary imports, a file one embeds, the bundle's
// manifest, and go.mod but its require and tool lines. A test, a fixture,
// a package it does not import and a dependency bump do not, and nor does
// what is not committed, which CI never sees.
func TestTheChangelogGateCountsWhatShips(t *testing.T) {
	const goMod = "go.mod"
	base := stalenessRepo(t)
	raw, err := os.ReadFile(filepath.Join(base, goMod))
	if err != nil {
		t.Fatal(err)
	}
	gomod := string(raw)
	bump := strings.Replace(gomod, "require example.com/dep v1.0.0", "require (\n\texample.com/dep v1.1.0 // a bump\n)", 1)
	for _, tc := range []struct {
		name string
		// files are committed, and after is written after the commit.
		files, after map[string]string
		ships        bool
	}{
		{"a package the binary imports", map[string]string{"internal/a/b.go": "package a\n\n// changed\n"}, nil, true},
		{"a file added to one", map[string]string{"internal/a/c.go": "package a\n"}, nil, true},
		{"a file removed from one", map[string]string{"internal/a/b.go": ""}, nil, true},
		{"a file moved out of one", map[string]string{"internal/a/b.go": "", "docs/b.go": "package a\n"}, nil, true},
		{"the command", map[string]string{"cmd/fixture/main.go": "package main\n\nfunc main() { println() }\n"}, nil, true},
		{"a file a package embeds", map[string]string{"internal/a/data/x.txt": "y\n"}, nil, true},
		{"go.mod's go line", map[string]string{goMod: strings.Replace(gomod, "go 1.21", "go 1.22", 1)}, nil, true},
		{"go.mod's toolchain", map[string]string{goMod: gomod + "\ntoolchain go1.21.0\n"}, nil, true},
		{"go.mod's replace", map[string]string{goMod: strings.Replace(gomod, "dep =>", "dep v1.0.0 =>", 1)}, nil, true},
		{"a dependency bump", map[string]string{goMod: bump}, nil, false},
		// The go.mod grammar reads ( as a token of its own (https://go.dev/ref/mod#go-mod-file-lexical).
		{"a dependency bump in a require( block", map[string]string{goMod: strings.Replace(gomod,
			"require example.com/dep v1.0.0", "require(\n\texample.com/dep v1.1.0\n)", 1)}, nil, false},
		// A tool directive names a tool `go tool` runs, which the binary does not
		// build (https://go.dev/ref/mod#go-mod-file-tool).
		{"a tool directive", map[string]string{goMod: gomod + "\ntool example.com/dep\n"}, nil, false},
		{"a tool( block", map[string]string{goMod: gomod + "\ntool(\n\texample.com/dep\n)\n"}, nil, false},
		{"a test", map[string]string{"internal/a/a_test.go": "package a\n\n// changed\n"}, nil, false},
		{"a fixture", map[string]string{"internal/a/testdata/x": "y\n"}, nil, false},
		// The go command builds a package from assembly, a .syso and C as well
		// as Go (`go help filetype`), so every file of its directory counts.
		{"a file of a package that it does not embed", map[string]string{"internal/a/notes.md": "more\n"}, nil, true},
		{"assembly and a .syso in one", map[string]string{"internal/a/a_amd64.s": "TEXT ·x(SB),0,$0\n", "internal/a/a.syso": "x\n"}, nil, true},
		{"a file in a directory under one that is no package", map[string]string{"internal/a/notes/x.md": "x\n"}, nil, false},
		{"the bundle's manifest", map[string]string{"packaging/mcpb/manifest.json": "{}\n"}, nil, true},
		// The bundle carries the root README, and it is still a doc.
		{"the root README", map[string]string{"README.md": "x\n"}, nil, false},
		{"a file beside the bundle's manifest", map[string]string{"packaging/mcpb/README.md": "x\n"}, nil, false},
		{"a package the binary does not import", map[string]string{"internal/unused/u.go": "package unused\n\n// changed\n"}, nil, false},
		// A dependency's package sits at a path of its own module's, never this one's.
		{"a package at the path of a dependency's", map[string]string{"tools.go": "package fixture\n\n// changed\n"}, nil, false},
		{"a script", map[string]string{"scripts/gates/main.go": "package main\n\nfunc main() { println() }\n"}, nil, false},
		{"a doc", map[string]string{"docs/x.md": "y\n"}, nil, false},
		{"a change not committed", nil, map[string]string{"internal/a/b.go": "package a\n\n// changed\n"}, false},
		{"go.mod as committed, its go line changed after", map[string]string{goMod: bump},
			map[string]string{goMod: strings.Replace(bump, "go 1.21", "go 1.22", 1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := stalenessCopy(t, base)
			t.Chdir(dir)
			var watched []string
			if tc.files != nil {
				stalenessChange(t, dir, tc.files)
				out, err := git("diff", "--name-only", "--no-renames", "-z", "HEAD~1", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				if watched, _, err = changelogWatched("HEAD~1", "HEAD", strings.FieldsFunc(out, func(r rune) bool { return r == 0 })); err != nil {
					t.Fatal(err)
				}
			}
			stalenessWrite(t, dir, tc.after)
			if ships := len(watched) > 0; ships != tc.ships {
				t.Errorf("changelogWatched() = %q, ships = %t; want %t", watched, ships, tc.ships)
			}
		})
	}
}

// With no binary and no docs, no rule reaches a verdict: the gate says so
// and exits 2. A real finding beside that is still a finding, and exits 1.
func TestTheStalenessGateNeedsItsInputs(t *testing.T) {
	const unread = "gates cannot read README.md"
	for _, tc := range []struct {
		name     string
		setupDoc string
		wantCode int
		want     []string
	}{
		{name: "nothing to judge", wantCode: 2, want: []string{unread}},
		{name: "a setup page that lists no scope", setupDoc: "# Setup\n", wantCode: 1,
			want: []string{"docs/gcp-setup.md does not list the scope openid", unread}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if tc.setupDoc != "" {
				put(t, dir, setupDocPath, tc.setupDoc)
			}
			var out, errOut bytes.Buffer
			code := run([]string{"staleness", filepath.Join(dir, "no-binary")}, &out, &errOut)
			problems := gatesProblems(t, errOut.String())
			if code != tc.wantCode || out.Len() != 0 || slices.ContainsFunc(tc.want, func(w string) bool {
				return !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, w) })
			}) {
				t.Errorf("exit %d, stdout %q, problems %q; want %d, nothing and %q among them",
					code, out.String(), problems, tc.wantCode, tc.want)
			}
		})
	}
}
