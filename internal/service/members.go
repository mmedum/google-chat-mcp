package service

import (
	"context"

	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// Member limits. The 200 is this server's, well under Google's
// own page cap.
const (
	defaultMemberLimit = 50
	maxMemberLimit     = 200
)

// MemberKind says whether a membership is a person or a Google Group.
type MemberKind string

// Member kinds.
const (
	KindHuman MemberKind = "HUMAN"
	KindGroup MemberKind = "GROUP"
)

// Membership roles and states, narrowed to what the tool surface
// promises. Anything Google adds arrives as the unspecified member
// rather than failing the row it rode in on.
var (
	// All three of Google's roles. ROLE_ASSISTANT_MANAGER was missing,
	// so an assistant manager's row reported no role at all — read as
	// "Google sent something we do not model" when in fact it was
	// modelled everywhere but here.
	memberRoles  = []string{gchat.RoleMember, gchat.RoleManager, gchat.RoleAssistantManager}
	memberStates = []string{"JOINED", "INVITED", "NOT_A_MEMBER"}
	// How someone relates to the organisation. Output only, and the
	// reason it is here: a space with EXTERNAL members is one to think
	// about before posting in.
	affiliations = []string{"INTERNAL", "EXTERNAL", "MANAGED_EXTERNAL"}
)

// Member is one row of a space's membership.
type Member struct {
	Kind MemberKind
	// Membership is the row's own resource name,
	// "spaces/{s}/members/{m}". It is what get_member,
	// update_member_role and remove_member take, and it was missing
	// from a listing until a live run showed those three asking for
	// something no tool here reported.
	Membership  string
	Name        string
	DisplayName string
	// Email is empty for a group, and for a person the People API
	// could not resolve.
	Email string
	Role  string
	State string
	// Affiliation is INTERNAL, EXTERNAL or MANAGED_EXTERNAL, and empty
	// when Google said nothing.
	Affiliation string
}

// ListMembersInput selects a page of a space's members.
type ListMembersInput struct {
	Space string
	Limit int
	// PageToken continues a previous call.
	PageToken string
}

// MembersResult is one page of a space's membership.
//
// A result rather than a bare slice: the page token was on the wire and
// dropped here, so "who is in this space" answered with the first 50 of
// 300 and nothing said it was a prefix.
type MembersResult struct {
	Members       []Member
	NextPageToken string
	// Unparsed is how many memberships were dropped as unmodellable. A
	// short list reads as a small space, which is what the count is for.
	Unparsed int
}

// ListMembers returns who is in a space, with emails resolved.
func (s *Service) ListMembers(ctx context.Context, in ListMembersInput) (*MembersResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	limit, err := clampLimit("limit", in.Limit, defaultMemberLimit, maxMemberLimit)
	if err != nil {
		return nil, err
	}

	// Google leaves out Google Groups and invited-but-not-joined people
	// unless asked, and both have to be asked for by name.
	//
	// This server asks. A space whose membership is mostly a Google
	// Group would otherwise answer "who is in this space" with almost
	// nobody, which is the silent-emptiness shape the enrichment rule
	// exists to prevent — and the caller can tell the three apart,
	// because every row carries kind and state.
	resp, err := s.client.ListMembers(ctx, gchat.ListMembersOptions{
		Space:       space,
		PageSize:    limit,
		ShowGroups:  true,
		ShowInvited: true,
		PageToken:   in.PageToken,
	})
	if err != nil {
		return nil, Classify(err)
	}

	ids := make([]string, 0, len(resp.Memberships))
	for _, m := range resp.Memberships {
		if m.Member != nil {
			ids = append(ids, m.Member.Name)
		}
	}
	people := s.resolvePeople(ctx, ids)

	out := make([]Member, 0, len(resp.Memberships))
	var unparsed int
	for _, m := range resp.Memberships {
		row := Member{
			Membership:  m.Name,
			Role:        narrowEnum(m.Role, memberRoles, "ROLE_UNSPECIFIED"),
			State:       narrowEnum(m.State, memberStates, "MEMBERSHIP_STATE_UNSPECIFIED"),
			Affiliation: narrowEnum(m.Affiliation, affiliations, ""),
		}
		switch {
		case m.Member != nil:
			person := people[m.Member.Name]
			row.Kind = KindHuman
			row.Name = m.Member.Name
			row.Email = person.Email
			row.DisplayName = person.DisplayName
			if row.DisplayName == "" {
				row.DisplayName = m.Member.DisplayName
			}
		case m.GroupMember != nil:
			row.Kind = KindGroup
			row.Name = m.GroupMember.Name
		default:
			// Neither a person nor a group: Google has added a third
			// kind of member. Dropping the row is the only honest
			// answer, but a short list reads as a small space, so the
			// count says otherwise.
			unparsed++
			continue
		}
		out = append(out, row)
	}
	s.warnUnparsed("memberships_unparsed", unparsed, len(resp.Memberships))
	return &MembersResult{Members: out, NextPageToken: resp.NextPageToken, Unparsed: unparsed}, nil
}

// Roles a caller may ask for, in the tool surface's spelling. Google's
// own values are longer and this maps to them, so a model does not have
// to know that ROLE_ prefix.
var rolesByName = map[string]string{
	"MEMBER":            gchat.RoleMember,
	"MANAGER":           gchat.RoleManager,
	"ASSISTANT_MANAGER": gchat.RoleAssistantManager,
}

// roleNames lists what a caller may pass, in a stable order.
var roleNames = []string{"MEMBER", "MANAGER", "ASSISTANT_MANAGER"}

// requireRole maps a caller's role onto Google's.
func requireRole(field, value string) (string, error) {
	if err := requireEnum(field, value, roleNames...); err != nil {
		return "", err
	}
	return rolesByName[value], nil
}

// GetMember returns one membership.
//
// list_members answers "who is in this space"; this answers "what is
// this membership", which is what a caller needs before changing a role
// or removing someone — a membership can name a Google Group, and
// removing that removes everyone in it.
func (s *Service) GetMember(ctx context.Context, name string) (*Member, error) {
	membership, err := requireMembership(name)
	if err != nil {
		return nil, err
	}
	got, err := s.client.GetMembership(ctx, membership)
	if err != nil {
		return nil, Classify(err)
	}
	row := Member{
		Membership:  got.Name,
		Name:        got.Name,
		Role:        narrowEnum(got.Role, memberRoles, "ROLE_UNSPECIFIED"),
		State:       narrowEnum(got.State, memberStates, "MEMBERSHIP_STATE_UNSPECIFIED"),
		Affiliation: narrowEnum(got.Affiliation, affiliations, ""),
	}
	switch {
	case got.Member != nil:
		row.Kind = KindHuman
		row.DisplayName = got.Member.DisplayName
		// One lookup, and a failure costs the address and nothing
		// else, which is the rule everywhere a person is resolved.
		if people := s.resolvePeople(ctx, []string{got.Member.Name}); len(people) > 0 {
			row.Email = people[got.Member.Name].Email
			if row.DisplayName == "" {
				row.DisplayName = people[got.Member.Name].DisplayName
			}
		}
	case got.GroupMember != nil:
		row.Kind = KindGroup
		row.Name = got.Name
		row.DisplayName = got.GroupMember.Name
	}
	return &row, nil
}

// UpdateMemberRoleInput names a membership and the role it should hold.
type UpdateMemberRoleInput struct {
	Membership string
	// Role is MEMBER, MANAGER or ASSISTANT_MANAGER.
	Role   string
	DryRun bool
}

// UpdateMemberRoleResult is the membership as it now stands.
type UpdateMemberRoleResult struct {
	Name string
	// Role is what Google recorded, or after a dry run what was asked
	// for.
	Role     string
	DryRun   bool
	Rendered map[string]any
}

// UpdateMemberRole changes what someone may do in a space.
//
// Role is the only field Google lets a user-authenticated caller patch,
// so there is no mask to choose and nothing else can be changed by
// accident.
func (s *Service) UpdateMemberRole(ctx context.Context, in UpdateMemberRoleInput) (*UpdateMemberRoleResult, error) {
	name, err := requireMembership(in.Membership)
	if err != nil {
		return nil, err
	}
	role, err := requireRole("role", in.Role)
	if err != nil {
		return nil, err
	}

	out := &UpdateMemberRoleResult{Name: name, Role: in.Role, DryRun: in.DryRun}
	if in.DryRun {
		rendered, err := renderBody(gchat.MembershipRequest{Role: role})
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}

	updated, err := s.client.UpdateMemberRole(ctx, name, role)
	if err != nil {
		return nil, Classify(err)
	}
	if updated.Role != "" {
		out.Role = updated.Role
	}
	return out, nil
}

// AddMemberInput invites one person, or adds a Google Group, into a
// space.
type AddMemberInput struct {
	Space string
	Email string
	// Group is "groups/{id}", whose id comes from the Cloud Identity
	// API. Exactly one of Email and Group.
	Group  string
	DryRun bool
}

// AddMemberResult is the membership that was made.
type AddMemberResult struct {
	// Name is empty after a dry run.
	Name  string
	Space string
	// Email and Group: whichever shape was used.
	Email string
	Group string
	// Role is what Google recorded, or after a dry run what was asked
	// for.
	Role     string
	DryRun   bool
	Rendered map[string]any
}

// AddMember invites someone into a space by email address.
//
// Someone already in the space is reported rather than passed off as a
// success. Google usually answers the duplicate with the existing
// membership, which belongs to whoever invited them first: returning it
// would say this call added them when it did not.
func (s *Service) AddMember(ctx context.Context, in AddMemberInput) (*AddMemberResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	// Any non-empty value counts for either shape, blank included: a
	// caller who sent a field meant to use it, and a value this server
	// cannot use should be reported rather than read as absent.
	var body *gchat.MembershipRequest
	out := &AddMemberResult{Space: space, DryRun: in.DryRun}
	switch {
	case in.Email != "" && in.Group != "":
		return nil, Invalidf("pass user_email or group_name, not both")
	case in.Group != "":
		group, err := requireGroup(in.Group)
		if err != nil {
			return nil, err
		}
		// Google's own rule, and worth saying here: a group cannot join
		// a group chat or a direct message, only a named space.
		out.Group = group
		body = gchat.BuildAddGroup(group, "")
	default:
		email, err := requireEmail("user_email", in.Email)
		if err != nil {
			return nil, err
		}
		out.Email = email
		body = gchat.BuildAddMember(email, "")
	}
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}

	membership, err := s.client.AddMember(ctx, space, body)
	if err != nil {
		subject := out.Email
		if subject == "" {
			subject = out.Group
		}
		if gchat.IsAlreadyExists(err) {
			return nil, Failf(ClassInvalid, "%s is already a member of %s.", subject, space)
		}
		return nil, directoryHint(subject+" is someone", Classify(err))
	}
	out.Name = membership.Name
	// What Google recorded. It ignores a role given on create — asked
	// for MANAGER against a live account on 2026-09-05, it recorded
	// ROLE_MEMBER and said nothing — so the argument is gone and this
	// reports the truth rather than the request.
	out.Role = membership.Role
	return out, nil
}

// membershipRemoved reports whether a membership Google handed back is
// one that has already ended.
//
// The message side of this was confirmed live; this is the same shape
// applied to a membership, which carries the same deleteTime and says
// NOT_A_MEMBER once someone has left.
func membershipRemoved(m *gchat.Membership) bool {
	return m != nil && (m.DeleteTime != "" || m.State == "NOT_A_MEMBER")
}

// RemoveMemberInput names a membership to delete.
type RemoveMemberInput struct {
	Membership string
	DryRun     bool
}

// RemoveMemberResult says whether anything was removed.
type RemoveMemberResult struct {
	Name string
	// Removed is false when the membership was already gone, and after
	// a dry run.
	Removed bool
	DryRun  bool
}

// RemoveMember takes someone out of a space, and succeeds when they
// were already out of it.
//
// As with a message, a refusal is checked rather than assumed: reading
// the membership back is what tells "already removed" from "you may not
// remove this person".
//
// A membership resource name is the only shape accepted. Matching on an
// email would mean resolving every member through the People API, which
// answers for the people the caller can see and stays silent about the
// rest: a miss would read as "already gone" when the person is still
// there. list_members gives the name.
func (s *Service) RemoveMember(ctx context.Context, in RemoveMemberInput) (*RemoveMemberResult, error) {
	name, err := requireMembership(in.Membership)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		return &RemoveMemberResult{Name: name, DryRun: true}, nil
	}
	removed, err := deleteIdempotent(ctx, name, s.client.RemoveMember,
		confirmGone(s.client.GetMembership, membershipRemoved))
	if err != nil {
		return nil, err
	}
	return &RemoveMemberResult{Name: name, Removed: removed}, nil
}
