package main

import (
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
