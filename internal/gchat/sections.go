package gchat

import (
	"context"
	"net/url"

	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

// SectionsParent addresses the caller's own sidebar. Google accepts
// "me" alongside an email or a numeric id and canonicalises it in the
// response, so nothing here has to know the caller's id.
const SectionsParent = "users/me/sections"

// AnySectionParent is Google's wildcard parent, which lists items
// across every section. It is documented only together with a space
// filter.
const AnySectionParent = SectionsParent + "/-"

// ListSectionsOptions narrows users.sections.list.
type ListSectionsOptions struct {
	PageSize  int
	PageToken string
}

// ListSections returns one page of the caller's sidebar sections.
func (c *Client) ListSections(ctx context.Context, o ListSectionsOptions) (*ListSectionsResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	var out ListSectionsResponse
	err := c.do(ctx, request{
		method: "GET",
		path:   SectionsParent,
		query:  q,
		scope:  scopes.UserSectionsReadonly,
	}, &out)
	return &out, err
}

// ListSectionItemsOptions narrows users.sections.items.list.
type ListSectionItemsOptions struct {
	// Section is "users/{u}/sections/{s}", or AnySectionParent to look
	// across all of them.
	Section string
	// Space filters to one space, which is how a caller finds which
	// section a space sits in.
	Space     string
	PageSize  int
	PageToken string
}

// ListSectionItems returns one page of the spaces filed under a section.
func (c *Client) ListSectionItems(ctx context.Context, o ListSectionItemsOptions) (*ListSectionItemsResponse, error) {
	parent := o.Section
	if parent == "" {
		parent = AnySectionParent
	}
	q := pageQuery(o.PageSize, o.PageToken)
	if o.Space != "" {
		// Unquoted, unlike every other filter here, and that is not an
		// oversight: quoting breaks the endpoint. Probed live against
		// three real space ids, the unquoted form answered 200 for all
		// three and the quoted form 400 "Invalid filter" for all three.
		// Google's own example is unquoted too.
		q.Set("filter", "space = "+o.Space)
	}
	var out ListSectionItemsResponse
	err := c.do(ctx, request{
		method: "GET",
		name:   parent,
		path:   "items",
		query:  q,
		scope:  scopes.UserSectionsReadonly,
	}, &out)
	return &out, err
}

// CustomSection is the only section type users.sections.create accepts.
// The system sections already exist and cannot be made again.
const CustomSection = "CUSTOM_SECTION"

// SectionRequest is the body of users.sections.create and .patch.
type SectionRequest struct {
	DisplayName string `json:"displayName"`
	// Type is set on create only. A patch masks displayName alone, so
	// sending a type there would be refused.
	Type string `json:"type,omitempty"`
}

// BuildCreateSection renders the body that adds a custom section.
func BuildCreateSection(displayName string) *SectionRequest {
	return &SectionRequest{DisplayName: displayName, Type: CustomSection}
}

// BuildRenameSection renders the body that renames one.
func BuildRenameSection(displayName string) *SectionRequest {
	return &SectionRequest{DisplayName: displayName}
}

// CreateSection adds a custom section to the caller's sidebar.
func (c *Client) CreateSection(ctx context.Context, body *SectionRequest) (*Section, error) {
	var out Section
	err := c.do(ctx, request{
		method: "POST",
		path:   SectionsParent,
		body:   body,
		scope:  scopes.UserSections,
	}, &out)
	return &out, err
}

// RenameSection changes a custom section's display name. name is
// "users/{u}/sections/{s}".
//
// The mask is fixed because Google lists no other patchable field.
func (c *Client) RenameSection(ctx context.Context, name string, body *SectionRequest) (*Section, error) {
	var out Section
	err := c.do(ctx, request{
		method: "PATCH",
		name:   name,
		query:  url.Values{"updateMask": {"displayName"}},
		body:   body,
		scope:  scopes.UserSections,
	}, &out)
	return &out, err
}

// DeleteSection removes a custom section. Its spaces are not deleted:
// Google files them back under the default sections.
func (c *Client) DeleteSection(ctx context.Context, name string) error {
	return c.deleteName(ctx, name, scopes.UserSections)
}

// PositionSectionRequest is the body of users.sections.position.
//
// Exactly one field is set. Google models the two as a union and
// answers 400 when both arrive.
type PositionSectionRequest struct {
	SortOrder        *int   `json:"sortOrder,omitempty"`
	RelativePosition string `json:"relativePosition,omitempty"`
}

// PositionSectionResponse is what a reorder returns.
type PositionSectionResponse struct {
	Section *Section `json:"section,omitempty"`
}

// PositionSection moves a section within the sidebar.
func (c *Client) PositionSection(ctx context.Context, name string, body *PositionSectionRequest) (*PositionSectionResponse, error) {
	var out PositionSectionResponse
	err := c.do(ctx, request{
		method: "POST",
		name:   name,
		verb:   "position",
		body:   body,
		scope:  scopes.UserSections,
	}, &out)
	return &out, err
}

// MoveSectionItemRequest is the body of users.sections.items.move.
type MoveSectionItemRequest struct {
	TargetSection string `json:"targetSection"`
}

// BuildMoveSectionItem renders the body that files an item elsewhere.
//
// It exists so the move follows the same rule as every other write here:
// one builder, whose value goes to both the dry-run preview and the
// request. A preview cannot then describe a body the move would not
// send.
func BuildMoveSectionItem(targetSection string) *MoveSectionItemRequest {
	return &MoveSectionItemRequest{TargetSection: targetSection}
}

// MoveSectionItemResponse is what a move returns.
type MoveSectionItemResponse struct {
	SectionItem *SectionItem `json:"sectionItem,omitempty"`
}

// MoveSectionItem files one item under another section. item is
// "users/{u}/sections/{s}/items/{i}".
func (c *Client) MoveSectionItem(ctx context.Context, item string, body *MoveSectionItemRequest) (*MoveSectionItemResponse, error) {
	var out MoveSectionItemResponse
	err := c.do(ctx, request{
		method: "POST",
		name:   item,
		verb:   "move",
		body:   body,
		scope:  scopes.UserSections,
	}, &out)
	return &out, err
}
