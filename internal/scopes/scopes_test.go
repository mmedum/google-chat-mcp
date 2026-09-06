package scopes

import (
	"slices"
	"strings"
	"testing"
)

func TestAllIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range All {
		if seen[s] {
			t.Errorf("duplicate scope %q", s)
		}
		seen[s] = true
		if s == OpenID || s == Email || s == Profile {
			continue
		}
		if !strings.HasPrefix(s, "https://www.googleapis.com/auth/") {
			t.Errorf("scope %q is not a Google scope URL", s)
		}
	}
	for _, want := range []string{OpenID, Email, Profile, Messages, Spaces, DirectoryReadonly} {
		if !seen[want] {
			t.Errorf("All is missing %q", want)
		}
	}
}

// Every scope named in the umbrella table must be one login asks for.
// A typo here would silently stop satisfying anything.
func TestImpliesOnlyNamesRequestedScopes(t *testing.T) {
	for umbrella, narrower := range implies {
		if !slices.Contains(All, umbrella) {
			t.Errorf("umbrella %q is not in All", umbrella)
		}
		for _, n := range narrower {
			if !slices.Contains(All, n) {
				t.Errorf("%q implies %q, which is not in All", umbrella, n)
			}
			if n == umbrella {
				t.Errorf("%q implies itself", umbrella)
			}
		}
	}
}

func TestSatisfied(t *testing.T) {
	for _, tc := range []struct {
		name     string
		required string
		granted  []string
		want     bool
	}{
		{"exact match", MessagesCreate, []string{MessagesCreate}, true},
		{"umbrella covers narrower", MessagesCreate, []string{Messages}, true},
		{"umbrella covers readonly", MessagesReadonly, []string{Messages}, true},
		{"spaces covers pins", Pins, []string{Spaces}, true},
		{"spaces readonly covers pins readonly", PinsReadonly, []string{SpacesReadonly}, true},
		{"narrower does not cover umbrella", Messages, []string{MessagesCreate}, false},
		{"unrelated scope", UserSections, []string{Messages, Spaces}, false},
		{"empty grant", MessagesReadonly, nil, false},
		{"among several", ReadState, []string{Messages, ReadState, Pins}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Satisfied(tc.required, tc.granted); got != tc.want {
				t.Errorf("Satisfied(%q, %v) = %v, want %v", tc.required, tc.granted, got, tc.want)
			}
		})
	}
}

func TestCanonicalRewritesOnlyTheAliases(t *testing.T) {
	got := Canonical([]string{OpenID, Email, Profile, Messages})
	want := []string{
		OpenID,
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		Messages,
	}
	if !slices.Equal(got, want) {
		t.Errorf("Canonical = %v, want %v", got, want)
	}
	if in := []string{Email}; Canonical(in)[0] == in[0] {
		t.Error("Canonical did not rewrite email")
	}
}

// Google reports email and profile back as URLs. Comparing a granted set
// against All without canonicalising both sides reports them missing.
func TestMissingIgnoresTheOIDCAliasSpelling(t *testing.T) {
	granted := Canonical(All)
	if missing := Missing(granted, false); len(missing) != 0 {
		t.Errorf("a full grant reports %d missing scopes: %v", len(missing), missing)
	}
	if missing := Missing(All, false); len(missing) != 0 {
		t.Errorf("a full grant in alias spelling reports missing: %v", missing)
	}
}

// The admin scope is asked for only when the admin toolset is on, so a
// token granted without it is complete for everyone else and short for
// the person who turned it on.
func TestMissingFollowsWhatLoginAskedFor(t *testing.T) {
	granted := Canonical(All)
	if missing := Missing(granted, false); len(missing) != 0 {
		t.Errorf("a full grant reports %v", missing)
	}
	missing := Missing(granted, true)
	if len(missing) != 1 || missing[0] != AdminSpacesReadonly {
		t.Errorf("with admin on, missing = %v, want the admin scope alone", missing)
	}
	if slices.Contains(All, AdminSpacesReadonly) {
		t.Error("the admin scope is in All, so every consent screen carries it")
	}
}

func TestMissingNamesWhatIsAbsent(t *testing.T) {
	granted := []string{OpenID, Email, Profile, Messages, Spaces, Memberships}
	missing := Missing(granted, false)
	if slices.Contains(missing, MessagesCreate) {
		t.Error("MessagesCreate is covered by the Messages umbrella")
	}
	if !slices.Contains(missing, UserSections) {
		t.Error("UserSections was not granted and should be reported")
	}
	if !slices.Contains(missing, DirectoryReadonly) {
		t.Error("DirectoryReadonly was not granted and should be reported")
	}
}
