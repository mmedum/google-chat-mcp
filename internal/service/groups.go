package service

import (
	"context"
	"errors"

	"github.com/mmedum/google-chat-mcp/v4/internal/gchat"
)

// Group is a Google Group as find_group and the member listings report
// it.
type Group struct {
	// Name is groups/{id}, which add_member takes as group_name.
	Name        string
	Email       string
	DisplayName string
}

// FindGroup turns a group's email address into the groups/{id} name
// add_member takes. Chat cannot do this: it names a group only by the
// Cloud Identity id, and has no lookup of its own.
//
// The lookup gives the name; a second read gives the group's display
// name. A failure of that read costs the display name and nothing else,
// since the name is the answer the caller came for.
func (s *Service) FindGroup(ctx context.Context, email string) (*Group, error) {
	email, err := requireEmail("email", email)
	if err != nil {
		return nil, err
	}
	name, err := s.client.LookupGroup(ctx, email)
	if err != nil {
		// Google answers 403 for an address no group has and, as far as
		// anyone outside can tell, for a group hidden from the caller
		// (live, 2026-10-03). 404 gets the same explanation in case that
		// changes.
		class := ClassNotFound
		switch {
		case gchat.IsForbidden(err):
			class = ClassForbidden
		case !gchat.IsNotFound(err):
			return nil, Classify(err)
		}
		return nil, Failf(class, "No group with the address %s is visible to this account. It may not "+
			"exist, or it may be one only its own members can see; its owner can say which.", email)
	}
	if !groupName.MatchString(name) {
		return nil, Failf(ClassUnexpected, "Cloud Identity answered the lookup with %q, which is not a group name.", name)
	}
	return &Group{Name: name, Email: email, DisplayName: s.resolveGroups(ctx, []string{name})[name].DisplayName}, nil
}

// degradation is what a failed lookup logs: its class and HTTP status,
// never its text, which can carry what was looked up.
func degradation(err error) (Class, int) {
	class, status := ClassUnexpected, 0
	var se *Error
	if errors.As(Classify(err), &se) {
		class = se.Class
	}
	var ae *gchat.APIError
	if errors.As(err, &ae) {
		status = ae.StatusCode
	}
	return class, status
}

// resolveGroups reads each group's address and display name from Cloud
// Identity, once per distinct group.
//
// A failure costs that group its address and name and nothing else, the
// same rule a person's lookup follows. A refusal that would fail every
// group the same way — a missing scope, a disabled API, a rejected token,
// a canceled call — ends the lookups rather than repeating once per row.
//
// The log line names the class and status, not Google's message, which
// for a disabled API names the Cloud project.
func (s *Service) resolveGroups(ctx context.Context, names []string) map[string]Group {
	out := make(map[string]Group, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, seen := out[name]; seen {
			continue
		}
		g, err := s.client.GetGroup(ctx, name)
		if err != nil {
			class, status := degradation(err)
			s.log.Warn("group_lookup_degraded", "class", class, "status", status)
			if gchat.IsMissingScope(err) || gchat.IsServiceDisabled(err) || gchat.IsUnauthorized(err) || ctx.Err() != nil {
				break
			}
			// Remembered empty, so a group on two rows is asked about once.
			out[name] = Group{}
			continue
		}
		out[name] = Group{Name: name, Email: g.GroupKey.ID, DisplayName: g.DisplayName}
	}
	return out
}
