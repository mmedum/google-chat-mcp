package gchat

import (
	"context"
	"net/url"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// ListMembersOptions narrows spaces.members.list.
type ListMembersOptions struct {
	// Space is "spaces/{id}".
	Space string
	// PageSize is capped by Google at 1000.
	PageSize int
	// PageToken continues a previous call.
	PageToken string
	// Filter is Google's filter expression over role and member type.
	Filter string
	// ShowGroups also returns Google Group memberships, and ShowInvited
	// also returns people who have been invited but have not joined.
	// Google leaves both off by default, so both have to be asked for.
	ShowGroups  bool
	ShowInvited bool
}

// ListMembers returns one page of a space's memberships.
func (c *Client) ListMembers(ctx context.Context, o ListMembersOptions) (*ListMembershipsResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	if o.ShowGroups {
		q.Set("showGroups", "true")
	}
	if o.ShowInvited {
		q.Set("showInvited", "true")
	}
	var out ListMembershipsResponse
	err := c.do(ctx, request{
		method: "GET",
		name:   o.Space,
		path:   "members",
		query:  q,
		scope:  scopes.MembershipsReadonly,
	}, &out)
	return &out, err
}

// MemberTypeHuman is the only member type a user-authenticated caller
// can add. Chat apps are added by an administrator, not through this.
const MemberTypeHuman = "HUMAN"

// The roles a membership can hold. Google's spelling, used unchanged
// from the wire to the tool surface.
const (
	RoleMember           = "ROLE_MEMBER"
	RoleManager          = "ROLE_MANAGER"
	RoleAssistantManager = "ROLE_ASSISTANT_MANAGER"
)

// MemberRef names a person in a membership request body.
type MemberRef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// GroupRef names a Google Group in a membership request body.
type GroupRef struct {
	Name string `json:"name"`
}

// MembershipRequest is the body of spaces.members.create, and one entry
// of the membership list spaces.setup takes.
//
// Exactly one of Member and GroupMember is set. Role is optional: the
// Membership resource documents it as settable, while the create guide
// does not mention it, so what Google recorded is read back off the
// response rather than assumed.
type MembershipRequest struct {
	Member      *MemberRef `json:"member,omitempty"`
	GroupMember *GroupRef  `json:"groupMember,omitempty"`
	Role        string     `json:"role,omitempty"`
}

// BuildAddMember renders the body that invites one person by email.
//
// Google resolves "users/{email}" to the account itself, so no
// directory lookup happens here. That matters: the People API answers
// for people the caller can see, and an invitation should not be
// limited to those.
func BuildAddMember(email, role string) *MembershipRequest {
	return &MembershipRequest{
		Member: &MemberRef{Name: "users/" + email, Type: MemberTypeHuman},
		Role:   role,
	}
}

// BuildAddGroup renders the body that adds a Google Group. group is
// "groups/{id}", whose id comes from the Cloud Identity API — Chat has
// no lookup of its own, and neither has this server.
func BuildAddGroup(group, role string) *MembershipRequest {
	return &MembershipRequest{GroupMember: &GroupRef{Name: group}, Role: role}
}

// UpdateMemberRole changes what someone may do in a space. name is
// "spaces/{s}/members/{m}".
//
// Role is the only field Google lets a user-authenticated caller patch,
// so the mask is fixed at it.
func (c *Client) UpdateMemberRole(ctx context.Context, name, role string) (*Membership, error) {
	var out Membership
	err := c.do(ctx, request{
		method: "PATCH",
		name:   name,
		query:  url.Values{"updateMask": {"role"}},
		body:   &MembershipRequest{Role: role},
		scope:  scopes.Memberships,
	}, &out)
	return &out, err
}

// AddMember invites someone into a space. space is "spaces/{id}".
func (c *Client) AddMember(ctx context.Context, space string, body *MembershipRequest) (*Membership, error) {
	var out Membership
	err := c.do(ctx, request{
		method: "POST",
		name:   space,
		path:   "members",
		body:   body,
		scope:  scopes.Memberships,
	}, &out)
	return &out, err
}

// GetMembership reads one membership. name is
// "spaces/{s}/members/{m}".
//
// Its only caller is the delete path, which uses a not-found answer to
// tell "already removed" from "you may not remove this": Google spells
// both as a 403 once a space keeps no history.
func (c *Client) GetMembership(ctx context.Context, name string) (*Membership, error) {
	var out Membership
	err := c.do(ctx, request{method: "GET", name: name, scope: scopes.MembershipsReadonly}, &out)
	return &out, err
}

// RemoveMember deletes a membership. name is "spaces/{s}/members/{m}".
func (c *Client) RemoveMember(ctx context.Context, name string) error {
	return c.deleteName(ctx, name, scopes.Memberships)
}
