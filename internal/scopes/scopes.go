// Package scopes is the single source of truth for the OAuth scopes this
// server requests and the scopes each tool needs.
//
// Google's tiers decide what a deployer pays for consent. Non-sensitive
// needs nothing. Sensitive needs a few days of self-service verification
// for a published app. Restricted needs an annual CASA review. An
// Internal Workspace app skips all of it, which is why this server asks
// for the umbrella scopes without apology; docs/security.md records the
// trade-off for anyone publishing externally.
package scopes

import "slices"

// OpenID Connect scopes. Google accepts "email" and "profile" on the
// authorization request and reports them back as URLs, which Canonical
// rewrites so a granted-scope comparison does not raise a false alarm.
const (
	OpenID  = "openid"
	Email   = "email"
	Profile = "profile"
)

// Chat scopes for the core surface.
const (
	// MessagesReadonly and the two below were split out of the Messages
	// umbrella, which is restricted tier. These three are sensitive.
	MessagesReadonly  = "https://www.googleapis.com/auth/chat.messages.readonly"
	MessagesCreate    = "https://www.googleapis.com/auth/chat.messages.create"
	MessagesReactions = "https://www.googleapis.com/auth/chat.messages.reactions"

	// Messages is restricted tier. Message edit and delete need it;
	// no granular scope covers them.
	Messages = "https://www.googleapis.com/auth/chat.messages"

	SpacesReadonly = "https://www.googleapis.com/auth/chat.spaces.readonly"
	// SpacesCreate covers spaces.setup, which find_direct_message uses
	// when no direct message exists yet.
	SpacesCreate = "https://www.googleapis.com/auth/chat.spaces.create"
	// Spaces is restricted tier. spaces.patch lists only the umbrella
	// for the user-authenticated path, so update_space needs it.
	Spaces = "https://www.googleapis.com/auth/chat.spaces"

	MembershipsReadonly = "https://www.googleapis.com/auth/chat.memberships.readonly"
	Memberships         = "https://www.googleapis.com/auth/chat.memberships"

	// Sections are per-user sidebar state. Nothing else in the set
	// covers them, and holding Spaces grants nothing here.
	UserSectionsReadonly = "https://www.googleapis.com/auth/chat.users.sections.readonly"
	UserSections         = "https://www.googleapis.com/auth/chat.users.sections"
)

// Chat scopes added for full API coverage. All sensitive except Delete.
const (
	// Delete is restricted tier and only delete_space needs it.
	Delete = "https://www.googleapis.com/auth/chat.delete"

	PinsReadonly = "https://www.googleapis.com/auth/chat.spaces.pins.readonly"
	Pins         = "https://www.googleapis.com/auth/chat.spaces.pins"

	CustomEmojisReadonly = "https://www.googleapis.com/auth/chat.customemojis.readonly"
	CustomEmojis         = "https://www.googleapis.com/auth/chat.customemojis"

	ReadStateReadonly = "https://www.googleapis.com/auth/chat.users.readstate.readonly"
	ReadState         = "https://www.googleapis.com/auth/chat.users.readstate"

	SpaceSettings = "https://www.googleapis.com/auth/chat.users.spacesettings"

	AvailabilityReadonly = "https://www.googleapis.com/auth/chat.users.availability.readonly"
	Availability         = "https://www.googleapis.com/auth/chat.users.availability"
)

// AdminSpacesReadonly searches every space in the Workspace, not only
// the caller's. It is the one scope that is not in All: only a
// Workspace administrator can use it, and login asks for its whole set
// in one prompt, so including it would put an administrator's scope on
// the consent screen of everyone who installs this binary. The admin
// toolset is what asks for it. See Requested.
const AdminSpacesReadonly = "https://www.googleapis.com/auth/chat.admin.spaces.readonly"

// People API scopes. DirectoryReadonly resolves same-domain colleagues;
// ContactsReadonly is the consumer-account fallback and covers the
// caller's own contacts.
const (
	DirectoryReadonly = "https://www.googleapis.com/auth/directory.readonly"
	ContactsReadonly  = "https://www.googleapis.com/auth/contacts.readonly"
)

// All is what login requests, in a stable order. Adding a scope here
// makes every existing token incomplete until the person logs in again,
// so a tool that needs it reports a [scope] error naming it.
var All = []string{
	OpenID,
	Email,
	Profile,

	MessagesReadonly,
	MessagesCreate,
	MessagesReactions,
	Messages,

	SpacesReadonly,
	SpacesCreate,
	Spaces,

	MembershipsReadonly,
	Memberships,

	UserSectionsReadonly,
	UserSections,

	Delete,
	PinsReadonly,
	Pins,
	CustomEmojisReadonly,
	CustomEmojis,
	ReadStateReadonly,
	ReadState,
	SpaceSettings,
	AvailabilityReadonly,
	Availability,

	DirectoryReadonly,
	ContactsReadonly,
}

// implies maps an umbrella scope to the narrower scopes it satisfies.
//
// Google accepts an umbrella wherever it accepts one of the scopes split
// out of it, verified against the Chat API discovery document. Without
// this table the server refuses calls Google would have allowed, and it
// refuses them for the people who paid the most for consent, since the
// umbrellas are the restricted-tier scopes.
var implies = map[string][]string{
	Messages:       {MessagesReadonly, MessagesCreate, MessagesReactions},
	Spaces:         {SpacesReadonly, SpacesCreate, PinsReadonly, Pins},
	Memberships:    {MembershipsReadonly},
	UserSections:   {UserSectionsReadonly},
	Pins:           {PinsReadonly},
	CustomEmojis:   {CustomEmojisReadonly},
	ReadState:      {ReadStateReadonly},
	Availability:   {AvailabilityReadonly},
	SpacesReadonly: {PinsReadonly},
}

// Satisfied reports whether granted covers required, directly or through
// an umbrella.
func Satisfied(required string, granted []string) bool {
	for _, g := range granted {
		if g == required {
			return true
		}
		for _, narrower := range implies[g] {
			if narrower == required {
				return true
			}
		}
	}
	return false
}

// canonical rewrites the OIDC aliases into the URLs Google reports back
// in a token response.
var canonical = map[string]string{
	Email:   "https://www.googleapis.com/auth/userinfo.email",
	Profile: "https://www.googleapis.com/auth/userinfo.profile",
}

// Canonical rewrites the email and profile aliases, leaving order and
// every other scope untouched. Comparing a granted set against All
// without this reports scopes as withheld that were in fact granted.
func Canonical(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		if c, ok := canonical[s]; ok {
			out[i] = c
			continue
		}
		out[i] = s
	}
	return out
}

// Requested is the scope set login asks for. admin adds the one scope
// All leaves out, and nothing else varies: a read-only server still
// asks for the write scopes, so turning writes back on does not cost a
// second consent screen.
func Requested(admin bool) []string {
	if !admin {
		return All
	}
	return append(slices.Clone(All), AdminSpacesReadonly)
}

// Missing returns the scopes login asked for that granted does not
// cover, in the requested order. Used by status and doctor to explain a
// stale token, so it has to ask about the same set login did.
func Missing(granted []string, admin bool) []string {
	g := Canonical(granted)
	var missing []string
	for _, want := range Canonical(Requested(admin)) {
		if !Satisfied(want, g) {
			missing = append(missing, want)
		}
	}
	return missing
}
