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

// Every scope named in the umbrella table must be one login can ask for.
// A typo here would silently stop satisfying anything. The widest set
// is admin with writes and deletes on, since the administrator scopes
// are deliberately not in All.
func TestImpliesOnlyNamesRequestedScopes(t *testing.T) {
	widest := Requested(true, false, true)
	for umbrella, narrower := range implies {
		if !slices.Contains(widest, umbrella) {
			t.Errorf("umbrella %q is not a scope login asks for", umbrella)
		}
		for _, n := range narrower {
			if !slices.Contains(widest, n) {
				t.Errorf("%q implies %q, which login never asks for", umbrella, n)
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
// against All without canonicalizing both sides reports them missing.
func TestMissingIgnoresTheOIDCAliasSpelling(t *testing.T) {
	granted := Canonical(All)
	if missing := Missing(granted, Requested(false, false, false)); len(missing) != 0 {
		t.Errorf("a full grant reports %d missing scopes: %v", len(missing), missing)
	}
	if missing := Missing(All, Requested(false, false, false)); len(missing) != 0 {
		t.Errorf("a full grant in alias spelling reports missing: %v", missing)
	}
}

// The administrator scopes are asked for only when the admin toolset is
// on, so a token granted without them is complete for everyone else and
// short for the person who turned it on.
func TestMissingFollowsWhatLoginAskedFor(t *testing.T) {
	granted := Canonical(All)
	if missing := Missing(granted, Requested(false, false, false)); len(missing) != 0 {
		t.Errorf("a full grant reports %v", missing)
	}
	if missing := Missing(granted, Requested(false, false, true)); len(missing) != 0 {
		t.Errorf("the destructive flag without admin asks for more: %v", missing)
	}
	missing := Missing(granted, Requested(true, false, false))
	want := []string{AdminSpacesReadonly, AdminMembershipsReadonly, AdminSpaces, AdminMemberships}
	if !slices.Equal(missing, want) {
		t.Errorf("with admin on, missing = %v, want %v", missing, want)
	}
	missing = Missing(granted, Requested(true, false, true))
	if !slices.Equal(missing, append(slices.Clone(want), AdminDelete)) {
		t.Errorf("with admin and deletes on, missing = %v, want %v and the delete scope", missing, want)
	}
	for _, s := range append(want, AdminDelete) {
		if slices.Contains(All, s) {
			t.Errorf("%s is in All, so every consent screen carries it", s)
		}
	}
}

// An umbrella covers its read: a person who granted the admin write
// scopes is not asked for the read-only ones again.
func TestAdminUmbrellasCoverTheirReads(t *testing.T) {
	granted := append(Canonical(All), AdminSpaces, AdminMemberships)
	if missing := Missing(granted, Requested(true, false, false)); len(missing) != 0 {
		t.Errorf("the admin umbrellas leave %v missing", missing)
	}
	if missing := Missing(append(Canonical(All), AdminSpacesReadonly, AdminMembershipsReadonly),
		Requested(true, false, false)); !slices.Equal(missing, []string{AdminSpaces, AdminMemberships}) {
		t.Errorf("a grant of the admin reads alone reports %v missing, want the two admin write scopes", missing)
	}
}

func TestMissingNamesWhatIsAbsent(t *testing.T) {
	granted := []string{OpenID, Email, Profile, Messages, Spaces, Memberships}
	missing := Missing(granted, Requested(false, false, false))
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

// A read-only server asks for a token that cannot write (the standard's
// §3: "requests read-only scopes"). The set is derived from the names,
// so this pins it to exactly the scopes that can only read.
func TestReadOnlyAsksForNothingThatWrites(t *testing.T) {
	want := []string{
		OpenID, Email, Profile,
		MessagesReadonly, SpacesReadonly, MembershipsReadonly, UserSectionsReadonly,
		PinsReadonly, CustomEmojisReadonly, ReadStateReadonly, AvailabilityReadonly,
		DirectoryReadonly, ContactsReadonly, GroupsReadonly,
	}
	if got := Requested(false, true, false); !slices.Equal(got, want) {
		t.Errorf("Requested(read-only) = %v, want %v", got, want)
	}
	// Read-only mode wins over the destructive flag: a read-only admin
	// server asks for the two admin reads and nothing that writes.
	wantAdmin := append(slices.Clone(want), AdminSpacesReadonly, AdminMembershipsReadonly)
	for _, destructive := range []bool{false, true} {
		if got := Requested(true, true, destructive); !slices.Equal(got, wantAdmin) {
			t.Errorf("Requested(admin, read-only, destructive=%v) = %v, want %v", destructive, got, wantAdmin)
		}
	}
	if got := Requested(false, false, false); !slices.Equal(got, All) {
		t.Errorf("Requested(writes on) = %v, want All", got)
	}
	// A full token is complete for a read-only server, and a read-only
	// token is short for one that writes.
	if missing := Missing(All, Requested(false, true, false)); len(missing) != 0 {
		t.Errorf("a full grant is short for a read-only server: %v", missing)
	}
	if missing := Missing(want, Requested(false, false, false)); !slices.Contains(missing, Messages) {
		t.Errorf("a read-only grant reports %v missing for a server that writes; want chat.messages among them", missing)
	}
}
