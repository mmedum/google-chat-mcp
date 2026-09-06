package gchat

import (
	"context"
	"net/url"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// Userinfo returns the OpenID Connect profile of the token's owner.
func (c *Client) Userinfo(ctx context.Context) (*UserInfo, error) {
	var out UserInfo
	err := c.do(ctx, request{api: oidcAPI, method: "GET", path: "userinfo", scope: scopes.Email}, &out)
	return &out, err
}

// ListSpacesOptions narrows spaces.list.
type ListSpacesOptions struct {
	// Filter is Google's filter expression, such as
	// `spaceType = "SPACE"`. Empty lists every space.
	Filter string
	// PageSize is capped by Google at 1000.
	PageSize int
	// PageToken continues a previous call.
	PageToken string
}

// ListSpaces returns one page of the spaces the caller belongs to.
//
// Pagination is never aggregated here. A tool that hides paging behind a
// loop can spend a caller's whole quota on one request, so the page
// token is surfaced and a follow-up is the caller's choice.
func (c *Client) ListSpaces(ctx context.Context, o ListSpacesOptions) (*ListSpacesResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	var out ListSpacesResponse
	err := c.do(ctx, request{method: "GET", path: "spaces", query: q, scope: scopes.SpacesReadonly}, &out)
	return &out, err
}

// GetSpace returns one space. name is "spaces/{id}".
func (c *Client) GetSpace(ctx context.Context, name string) (*Space, error) {
	var out Space
	err := c.do(ctx, request{method: "GET", name: name, scope: scopes.SpacesReadonly}, &out)
	return &out, err
}

// FindDirectMessage returns the direct message with one person, or a
// not-found error when none exists yet. user is "users/{id}" or an
// email address.
func (c *Client) FindDirectMessage(ctx context.Context, user string) (*Space, error) {
	q := url.Values{"name": {user}}
	var out Space
	err := c.do(ctx, request{
		method: "GET",
		path:   "spaces",
		verb:   "findDirectMessage",
		query:  q,
		scope:  scopes.SpacesReadonly,
	}, &out)
	return &out, err
}

// Search limits Google documents for spaces.search, checked against the
// REST reference on 2026-09-05: a page holds 100 without admin access
// and 1000 with it, and a query is at most 1000 characters.
const (
	MaxSearchSpacesPageSize      = 100
	MaxAdminSearchSpacesPageSize = 1000
	MaxSearchSpacesQuery         = 1000
)

// SearchSpacesOptions narrows spaces.search.
type SearchSpacesOptions struct {
	// Query is Google's search expression and is required. Without
	// admin access it may only mention displayName, externalUserAllowed
	// and spaceType, and spaceType must be SPACE.
	Query string
	// OrderBy is "membershipCount.joined_direct_human_user_count desc",
	// "lastActiveTime desc" or "createTime desc". Empty takes Google's
	// order.
	OrderBy string
	// PageSize is capped by Google, see the constants above.
	PageSize int
	// PageToken continues a previous call. Google returns one only
	// under admin access.
	PageToken string
	// UseAdminAccess searches every space in the Workspace rather than
	// the caller's own, and needs an administrator's scope. The admin
	// toolset is what allows it.
	UseAdminAccess bool
}

// SearchSpaces finds named spaces by display name and metadata.
//
// It reaches spaces the caller is not a member of, which list_spaces
// cannot, and it finds only named spaces: Google's own grammar requires
// spaceType = "SPACE". Group chats are what find_group_chats is for.
func (c *Client) SearchSpaces(ctx context.Context, o SearchSpacesOptions) (*SearchSpacesResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	q.Set("query", o.Query)
	if o.OrderBy != "" {
		q.Set("orderBy", o.OrderBy)
	}
	scope := scopes.SpacesReadonly
	if o.UseAdminAccess {
		q.Set("useAdminAccess", "true")
		scope = scopes.AdminSpacesReadonly
	}
	var out SearchSpacesResponse
	err := c.do(ctx, request{
		method: "GET",
		path:   "spaces",
		verb:   "search",
		query:  q,
		scope:  scope,
	}, &out)
	return &out, err
}

// Group chat search limits Google documents for spaces.findGroupChats,
// checked against the REST reference on 2026-09-05.
const (
	MaxFindGroupChatsUsers    = 49
	MaxFindGroupChatsPageSize = 30
)

// FindGroupChatsOptions narrows spaces.findGroupChats.
type FindGroupChatsOptions struct {
	// Users are "users/{id}" or "users/{email}", at most
	// MaxFindGroupChatsUsers of them. The caller is not named: Google
	// matches group chats whose human members are exactly the caller
	// plus these people.
	Users []string
	// PageSize is capped by Google at MaxFindGroupChatsPageSize.
	PageSize int
	// PageToken continues a previous call.
	PageToken string
}

// FindGroupChats returns the group chats holding exactly the caller and
// the people named.
//
// The expanded view is asked for so a row carries the space's type and
// display name rather than a bare resource name. Google documents that
// it additionally needs a scope that reads space data, which login
// requests alongside the membership scope named here.
func (c *Client) FindGroupChats(ctx context.Context, o FindGroupChatsOptions) (*FindGroupChatsResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	q.Set("spaceView", "SPACE_VIEW_EXPANDED")
	for _, u := range o.Users {
		q.Add("users", u)
	}
	var out FindGroupChatsResponse
	err := c.do(ctx, request{
		method: "GET",
		path:   "spaces",
		verb:   "findGroupChats",
		query:  q,
		scope:  scopes.MembershipsReadonly,
	}, &out)
	return &out, err
}

// Space types spaces.setup accepts. Google's spelling, used unchanged
// from the wire to the tool surface.
const (
	SpaceTypeSpace         = "SPACE"
	SpaceTypeGroupChat     = "GROUP_CHAT"
	SpaceTypeDirectMessage = "DIRECT_MESSAGE"
)

// SetupSpaceRequest is the body of spaces.setup, which creates a space
// and its first members in one call.
type SetupSpaceRequest struct {
	Space *SetupSpace `json:"space"`
	// Memberships never carries the caller: Google adds the
	// authenticated user itself.
	Memberships []MembershipRequest `json:"memberships"`
}

// SetupSpace is the space half of a spaces.setup body.
type SetupSpace struct {
	SpaceType string `json:"spaceType"`
	// DisplayName belongs only to a named space. Google answers 400
	// when a direct message or a group chat carries one, which is the
	// answer a caller who sent one should get: whether an argument is
	// allowed is decided in internal/service, and dropping it here
	// would hide the mistake rather than report it.
	DisplayName string `json:"displayName,omitempty"`
}

// BuildSetupSpace renders the body that creates a space.
func BuildSetupSpace(spaceType, displayName string, memberEmails []string) *SetupSpaceRequest {
	body := &SetupSpaceRequest{
		Space:       &SetupSpace{SpaceType: spaceType, DisplayName: displayName},
		Memberships: make([]MembershipRequest, 0, len(memberEmails)),
	}
	for _, email := range memberEmails {
		body.Memberships = append(body.Memberships, *BuildAddMember(email, ""))
	}
	return body
}

// SetupSpace creates a space with its first members.
func (c *Client) SetupSpace(ctx context.Context, body *SetupSpaceRequest) (*Space, error) {
	var out Space
	err := c.do(ctx, request{
		method: "POST",
		path:   "spaces",
		verb:   "setup",
		body:   body,
		scope:  scopes.SpacesCreate,
	}, &out)
	return &out, err
}

// UpdateSpaceRequest is the body of spaces.patch.
type UpdateSpaceRequest struct {
	DisplayName *string             `json:"displayName,omitempty"`
	Details     *UpdateSpaceDetails `json:"spaceDetails,omitempty"`
}

// UpdateSpaceDetails is the spaceDetails half of a patch body.
//
// Description carries no omitempty, unlike the response type: clearing
// a description is a real edit, and omitting the field would send an
// empty object that changes nothing.
type UpdateSpaceDetails struct {
	Description string `json:"description"`
}

// BuildUpdateSpace renders a space patch and the mask that goes with
// it. A nil argument is a field the caller did not ask to change.
//
// Google's updateMask takes only top-level paths here, so editing the
// description masks the whole spaceDetails object and clears anything
// else in it that was not resent. update_space says so; there is no
// mask that would avoid it.
func BuildUpdateSpace(displayName, description *string) (*UpdateSpaceRequest, string) {
	body := &UpdateSpaceRequest{}
	mask := make([]string, 0, 2)
	if displayName != nil {
		body.DisplayName = displayName
		mask = append(mask, "displayName")
	}
	if description != nil {
		body.Details = &UpdateSpaceDetails{Description: *description}
		mask = append(mask, "spaceDetails")
	}
	return body, strings.Join(mask, ",")
}

// UpdateSpace patches a space. name is "spaces/{id}".
//
// It needs the restricted-tier chat.spaces umbrella: spaces.patch lists
// nothing narrower for a user-authenticated caller.
func (c *Client) UpdateSpace(ctx context.Context, name string, body *UpdateSpaceRequest, mask string) (*Space, error) {
	var out Space
	err := c.do(ctx, request{
		method: "PATCH",
		name:   name,
		query:  url.Values{"updateMask": {mask}},
		body:   body,
		scope:  scopes.Spaces,
	}, &out)
	return &out, err
}
