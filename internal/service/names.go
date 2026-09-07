package service

import (
	"regexp"
	"strings"
)

// Resource names are checked before anything reaches Google.
//
// Two reasons, and the second is the one that matters. Google answers a
// malformed name with a 400 or a 404 whose message says nothing useful,
// so catching it here is the only place the argument's own name is
// still known. And several calls put a name inside a Chat filter
// expression — `thread.name = "…"`, `space = …` — where an unchecked
// quote would end the clause early. Nothing reachable that way crosses
// a space or a user, because the parent is fixed in the request path,
// but a filter the caller wrote is not one this server should send.
//
// The pattern the tool schemas enforce: an id is
// letters, digits, dots, underscores and hyphens, and holds at least
// one letter or digit.
//
// That last clause is load-bearing, and it was not written for the
// reason it now matters. Chat spells its wildcards as a lone hyphen —
// "spaces/-" searches every space, "users/me/sections/-" every section
// — so an id that could be a bare hyphen would let a caller widen a
// call from one resource to all of them by spelling the wildcard.
// Requiring a letter or a digit refuses it. The rule is asserted in
// names_test.go rather than left to be inferred from the pattern; a
// sibling server found the same class of bug where an id could spell a
// neighbouring endpoint, protected only by a lookup three layers away
// that happened to fail first.
var (
	idPattern     = `[A-Za-z0-9._-]*[A-Za-z0-9][A-Za-z0-9._-]*`
	spaceName     = regexp.MustCompile(`^spaces/` + idPattern + `$`)
	messageName   = regexp.MustCompile(`^spaces/` + idPattern + `/messages/` + idPattern + `$`)
	threadName    = regexp.MustCompile(`^spaces/` + idPattern + `/threads/` + idPattern + `$`)
	sectionName   = regexp.MustCompile(`^users/` + idPattern + `/sections/` + idPattern + `$`)
	groupName     = regexp.MustCompile(`^groups/` + idPattern + `$`)
	emojiName     = regexp.MustCompile(`^customEmojis/` + idPattern + `$`)
	bareIDPattern = regexp.MustCompile(`^` + idPattern + `$`)
)

// requireSpace checks that an argument names a space.
//
// A bare id is accepted: a model that has seen "spaces/AAA" once tends
// to send "AAA" back.
func requireSpace(value string) (string, error) {
	v := strings.TrimSpace(value)
	if bareIDPattern.MatchString(v) {
		v = "spaces/" + v
	}
	return requireShape("space_id", v, spaceName, "spaces/{space}")
}

// requireMessage checks that an argument names a message.
//
// Unlike a space, there is no bare form to accept: a message id means
// nothing without the space it lives in.
func requireMessage(value string) (string, error) {
	return requireShape("message_name", value, messageName, "spaces/{space}/messages/{message}")
}

// requireThread checks that an argument names a thread in space.
//
// The space has to match. Google lists messages under a space and
// answers 400 when the thread belongs to a different one, which is a
// worse way to learn it.
func requireThread(space, value string) (string, error) {
	v, err := requireShape("thread_name", value, threadName, "spaces/{space}/threads/{thread}")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(v, space+"/threads/") {
		return "", Invalidf("thread_name %q is not in %s", value, space)
	}
	return v, nil
}

// requireCustomEmoji checks that an argument names a custom emoji.
//
// The resource name, not the :shortcode:. list_custom_emojis reports
// both, and only one of them addresses anything.
//
// The field is named by the caller rather than fixed, because the two
// tools that reach here spell it `name` while this said `emoji_name`.
// The model read the error, retried with the field the error named, and
// got an SDK schema rejection with no class tag and no hint — two turns
// to reach a dead end, from a message that was simply wrong about the
// argument it was describing.
func requireCustomEmoji(field, value string) (string, error) {
	return requireShape(field, value, emojiName, "customEmojis/{emoji}")
}

// requireGroup checks that an argument names a Google Group.
//
// No bare form: a group id is a Cloud Identity id with no shape of its
// own, so "groups/" is the only thing that says what the value is. This
// server cannot look one up — Chat has no group lookup and neither has
// the People API — so the caller brings it.
func requireGroup(value string) (string, error) {
	return requireShape("group_name", value, groupName, "groups/{group}")
}

// requireSection checks that an argument names a sidebar section.
func requireSection(value string) (string, error) {
	return requireShape("section_name", value, sectionName, "users/{user}/sections/{section}")
}

// requireShape is the common check: present, and the right shape.
func requireShape(field, value string, pattern *regexp.Regexp, shape string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", Invalidf("%s is required", field)
	}
	if !pattern.MatchString(v) {
		return "", Invalidf("%s %q must be a %s resource name", field, value, shape)
	}
	return v, nil
}

// Names this server accepts but never lists. A membership, a reaction
// and a section item are all addressed by a name the caller read out of
// another tool's result, so the shape is the only check worth making.
var (
	membershipName = regexp.MustCompile(`^spaces/` + idPattern + `/members/` + idPattern + `$`)
	reactionName   = regexp.MustCompile(`^spaces/` + idPattern + `/messages/` + idPattern + `/reactions/` + idPattern + `$`)
	// A section item's id is base64url of the space resource name, so
	// it can carry padding that no other id here does.
	itemIDPattern   = `[A-Za-z0-9._=-]*[A-Za-z0-9][A-Za-z0-9._=-]*`
	sectionItemName = regexp.MustCompile(`^users/` + idPattern + `/sections/` + idPattern + `/items/(?:spaces/)?` + itemIDPattern + `$`)
	spaceEventName  = regexp.MustCompile(`^spaces/` + idPattern + `/spaceEvents/` + idPattern + `$`)
)

// requireSpaceEvent checks that an argument names a space event.
func requireSpaceEvent(value string) (string, error) {
	return requireShape("event_name", value, spaceEventName, "spaces/{space}/spaceEvents/{event}")
}

// requireMembership checks that an argument names a membership.
func requireMembership(value string) (string, error) {
	return requireShape("membership_name", value, membershipName, "spaces/{space}/members/{member}")
}

// requireReaction checks that an argument names a reaction.
func requireReaction(value string) (string, error) {
	return requireShape("reaction_name", value, reactionName, "spaces/{space}/messages/{message}/reactions/{reaction}")
}

// requireSectionItem checks that an argument names a section item.
func requireSectionItem(value string) (string, error) {
	return requireShape("item_name", value, sectionItemName,
		"users/{user}/sections/{section}/items/{item}")
}

// emailPattern is what this server will put in a "users/{email}"
// resource name.
//
// It is deliberately narrower than an address may legally be. The value
// is interpolated into a request body and into a resource name, so the
// characters that could end a quoted string or a path segment are
// refused rather than escaped; nobody in a Workspace directory needs
// them.
var emailPattern = regexp.MustCompile(`^[^\s@"'\\<>,;/]+@[^\s@"'\\<>,;/]+\.[^\s@"'\\<>,;/]+$`)

// requireEmail checks that an argument is an address this server can
// address someone by.
func requireEmail(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", Invalidf("%s is required", field)
	}
	if !emailPattern.MatchString(v) {
		return "", Invalidf("%s %q is not an email address", field, value)
	}
	return v, nil
}
