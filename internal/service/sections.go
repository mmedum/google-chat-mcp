package service

import (
	"context"
	"strings"

	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

// Section listing limits, as the tool schema documents them.
const (
	defaultSectionLimit = 50
	maxSectionLimit     = 100
)

// Section types, narrowed to what the tool surface promises.
var sectionTypes = []string{
	"CUSTOM_SECTION",
	"DEFAULT_DIRECT_MESSAGES",
	"DEFAULT_SPACES",
	"DEFAULT_APPS",
}

// systemSectionLabels name the sections Google creates. It gives them
// no display name of their own, and a parenthesised label is what keeps
// a custom section called "Spaces" apart from the system one.
var systemSectionLabels = map[string]string{
	"DEFAULT_DIRECT_MESSAGES": "(direct messages)",
	"DEFAULT_SPACES":          "(spaces)",
	"DEFAULT_APPS":            "(apps)",
}

// Section is one group in the caller's sidebar.
type Section struct {
	Name        string
	DisplayName string
	Type        string
	// SortOrder is Google's output-only rank. Nil when it said nothing.
	SortOrder *int
}

// ListSectionsInput selects a page of the caller's sections.
type ListSectionsInput struct {
	Limit     int
	PageToken string
}

// ListSectionsResult is a page of sections.
//
// Unparsed counts rows this server could not read. Non-zero means the
// page is incomplete, which a bare list could not say: a short list
// otherwise reads as a short sidebar.
type ListSectionsResult struct {
	Sections      []Section
	NextPageToken string
	Unparsed      int
}

// ListSections returns the caller's own sidebar sections.
func (s *Service) ListSections(ctx context.Context, in ListSectionsInput) (*ListSectionsResult, error) {
	limit, err := clampLimit("limit", in.Limit, defaultSectionLimit, maxSectionLimit)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.ListSections(ctx, gchat.ListSectionsOptions{PageSize: limit, PageToken: in.PageToken})
	if err != nil {
		return nil, Classify(err)
	}

	out := &ListSectionsResult{
		Sections:      make([]Section, 0, len(resp.Sections)),
		NextPageToken: resp.NextPageToken,
	}
	for _, sec := range resp.Sections {
		if sec.Name == "" {
			out.Unparsed++
			continue
		}
		kind := narrowEnum(sec.Type, sectionTypes, "SECTION_TYPE_UNSPECIFIED")
		out.Sections = append(out.Sections, Section{
			Name:        sec.Name,
			DisplayName: sectionLabel(sec.DisplayName, kind),
			Type:        kind,
			SortOrder:   sec.SortOrder,
		})
	}
	s.warnUnparsed("sections_unparsed", out.Unparsed, len(resp.Sections))
	return out, nil
}

// sectionLabel is what to call a section.
func sectionLabel(displayName, kind string) string {
	if displayName != "" {
		return displayName
	}
	if label, ok := systemSectionLabels[kind]; ok {
		return label
	}
	return "(unnamed section)"
}

// SectionItem is one space filed under a section.
type SectionItem struct {
	Name        string
	SectionName string
	// Space is empty when the item is not a space. Google's item field
	// is a union and only spaces are members of it today.
	Space string
}

// ListSectionItemsInput selects which items to list.
//
// One of Section and Space is required. Google documents the wildcard
// parent only together with a space filter, and an unfiltered wildcard
// listing is not a shape this server can promise anything about.
type ListSectionItemsInput struct {
	Section   string
	Space     string
	Limit     int
	PageToken string
}

// ListSectionItemsResult is a page of section items.
type ListSectionItemsResult struct {
	Items         []SectionItem
	NextPageToken string
	Unparsed      int
}

// ListSectionItems lists the spaces under one section, or finds which
// section one space sits in.
func (s *Service) ListSectionItems(ctx context.Context, in ListSectionItemsInput) (*ListSectionItemsResult, error) {
	section, space := strings.TrimSpace(in.Section), strings.TrimSpace(in.Space)
	if section == "" && space == "" {
		return nil, Invalidf("pass section_name to read one section, space_id to find where one space sits, or both")
	}
	var err error
	if section != "" {
		if section, err = requireSection(section); err != nil {
			return nil, err
		}
	}
	if space != "" {
		if space, err = requireSpace(space); err != nil {
			return nil, err
		}
	}
	limit, err := clampLimit("limit", in.Limit, defaultSectionLimit, maxSectionLimit)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.ListSectionItems(ctx, gchat.ListSectionItemsOptions{
		Section:   section,
		Space:     space,
		PageSize:  limit,
		PageToken: in.PageToken,
	})
	if err != nil {
		return nil, Classify(err)
	}

	out := &ListSectionItemsResult{
		Items:         make([]SectionItem, 0, len(resp.SectionItems)),
		NextPageToken: resp.NextPageToken,
	}
	for _, item := range resp.SectionItems {
		parent := sectionOfItem(item.Name)
		if item.Name == "" || parent == "" {
			out.Unparsed++
			continue
		}
		out.Items = append(out.Items, SectionItem{
			Name:        item.Name,
			SectionName: parent,
			Space:       item.Space,
		})
	}
	s.warnUnparsed("section_items_unparsed", out.Unparsed, len(resp.SectionItems))
	return out, nil
}

// maxSectionDisplayName is the bound on a section's name.
const maxSectionDisplayName = 80

// Relative positions users.sections.position accepts.
const (
	PositionStart = "START"
	PositionEnd   = "END"
)

// CreateSectionInput is a new custom section.
type CreateSectionInput struct {
	DisplayName string
	DryRun      bool
}

// SectionWriteResult is what a create or a rename produced.
type SectionWriteResult struct {
	// Name is empty after a dry-run create: Google assigns the id.
	Name        string
	DisplayName string
	DryRun      bool
	Rendered    map[string]any
}

// CreateSection adds a custom section to the caller's sidebar.
//
// Google does not deduplicate on name, so two calls leave two sections
// with the same label. The tool description says to list first.
func (s *Service) CreateSection(ctx context.Context, in CreateSectionInput) (*SectionWriteResult, error) {
	name, err := requireDisplayName(in.DisplayName, maxSectionDisplayName)
	if err != nil {
		return nil, err
	}

	body := gchat.BuildCreateSection(name)
	out := &SectionWriteResult{DisplayName: name, DryRun: in.DryRun}
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}

	created, err := s.client.CreateSection(ctx, body)
	if err != nil {
		return nil, Classify(err)
	}
	out.Name = created.Name
	return out, nil
}

// RenameSectionInput is a new label for one section.
type RenameSectionInput struct {
	Section     string
	DisplayName string
	DryRun      bool
}

// RenameSection changes a custom section's label.
//
// Only a custom section can be renamed. Google's refusal of a system
// section is left to speak for itself rather than being guessed at from
// a resource id.
func (s *Service) RenameSection(ctx context.Context, in RenameSectionInput) (*SectionWriteResult, error) {
	section, err := requireSection(in.Section)
	if err != nil {
		return nil, err
	}
	name, err := requireDisplayName(in.DisplayName, maxSectionDisplayName)
	if err != nil {
		return nil, err
	}

	body := gchat.BuildRenameSection(name)
	out := &SectionWriteResult{Name: section, DisplayName: name, DryRun: in.DryRun}
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}

	renamed, err := s.client.RenameSection(ctx, section, body)
	if err != nil {
		return nil, Classify(err)
	}
	if renamed.Name != "" {
		out.Name = renamed.Name
	}
	return out, nil
}

// DeleteSectionInput names a section to delete.
type DeleteSectionInput struct {
	Section string
	DryRun  bool
}

// DeleteSectionResult says whether anything was deleted.
type DeleteSectionResult struct {
	Name string
	// Deleted is false when the section was already gone, and after a
	// dry run.
	Deleted bool
	DryRun  bool
}

// DeleteSection removes a custom section, and succeeds when it was
// already gone.
//
// A 403 is not "already gone" here, unlike for a message: the one that
// turns up is Google refusing to delete a system section, and swallowing
// it would report a deletion that never happened and never could.
//
// The spaces in the section are not deleted. Google files them back
// under the default sections.
func (s *Service) DeleteSection(ctx context.Context, in DeleteSectionInput) (*DeleteSectionResult, error) {
	section, err := requireSection(in.Section)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		return &DeleteSectionResult{Name: section, DryRun: true}, nil
	}
	// No confirmGone: a section's 403 is Google declining to delete a
	// system section, and that is the caller's to see.
	deleted, err := deleteIdempotent(ctx, section, s.client.DeleteSection, nil)
	if err != nil {
		return nil, err
	}
	return &DeleteSectionResult{Name: section, Deleted: deleted}, nil
}

// PositionSectionInput moves a section in the sidebar. Exactly one of
// SortOrder and Relative is set.
type PositionSectionInput struct {
	Section   string
	SortOrder *int
	Relative  string
	DryRun    bool
}

// PositionSectionResult is where the section ended up.
type PositionSectionResult struct {
	Name string
	// SortOrder is the rank Google reports after the move, when it
	// reports one. Nil after a dry run: nothing moved, so the rank
	// asked for is in the rendered body instead.
	SortOrder *int
	DryRun    bool
	Rendered  map[string]any
}

// PositionSection moves a section up or down the sidebar.
//
// The two ways of saying where are a union upstream, so sending both is
// a 400 and is refused here instead.
func (s *Service) PositionSection(ctx context.Context, in PositionSectionInput) (*PositionSectionResult, error) {
	section, err := requireSection(in.Section)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimSpace(in.Relative)
	switch {
	case in.SortOrder != nil && relative != "":
		return nil, Invalidf("pass sort_order or relative_position, not both")
	case in.SortOrder == nil && relative == "":
		return nil, Invalidf("pass sort_order (1 or above) or relative_position (START or END)")
	case in.SortOrder != nil && *in.SortOrder < 1:
		return nil, Invalidf("sort_order %d must be 1 or above", *in.SortOrder)
	case relative != "":
		if err := requireEnum("relative_position", relative, PositionStart, PositionEnd); err != nil {
			return nil, err
		}
	}

	body := &gchat.PositionSectionRequest{SortOrder: in.SortOrder, RelativePosition: relative}
	out := &PositionSectionResult{Name: section, DryRun: in.DryRun}
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}

	moved, err := s.client.PositionSection(ctx, section, body)
	if err != nil {
		return nil, Classify(err)
	}
	if moved.Section != nil {
		// Informational: the move has already landed, so a response
		// without a rank must not turn it into an error.
		out.SortOrder = moved.Section.SortOrder
	}
	return out, nil
}

// MoveSpaceToSectionInput files one space under one section.
type MoveSpaceToSectionInput struct {
	Space   string
	Section string
	// Item is the space's current section item, when the caller
	// already knows it. It skips the lookup.
	Item   string
	DryRun bool
}

// MoveSpaceToSectionResult says where the space was and where it went.
type MoveSpaceToSectionResult struct {
	Space   string
	Section string
	Item    string
	// From is where the space sat before the move, resolved before
	// anything is written and so populated on a dry run too.
	From string
	// Moved is false in three cases: a dry run, a space already in the
	// target section, and both at once.
	Moved    bool
	DryRun   bool
	Rendered map[string]any
}

// MoveSpaceToSection files a space under a section in the caller's own
// sidebar.
//
// Every space already sits in some section, a default one until it is
// filed, so this both files and re-files. It is a no-op when the space
// is already where it is wanted.
//
// Passing item_name skips the lookup, which is what turns a bulk sort
// from two requests per space into one listing per section plus one
// write per space that actually moves. Nothing re-checks that the item
// belongs to the space: checking would cost the lookup the hint exists
// to avoid, and both are the caller's own sidebar under their own
// token, so a mismatched pair misfiles rather than reaching anything
// else.
func (s *Service) MoveSpaceToSection(ctx context.Context, in MoveSpaceToSectionInput) (*MoveSpaceToSectionResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	section, err := requireSection(in.Section)
	if err != nil {
		return nil, err
	}
	item := in.Item
	if item != "" {
		if item, err = requireSectionItem(item); err != nil {
			return nil, err
		}
	} else if item, err = s.locateSectionItem(ctx, space); err != nil {
		return nil, err
	}

	from := sectionOfItem(item)
	out := &MoveSpaceToSectionResult{
		Space:   space,
		Section: section,
		Item:    item,
		From:    from,
		DryRun:  in.DryRun,
	}
	needsMove := !sameSection(from, section)
	body := gchat.BuildMoveSectionItem(section)
	if in.DryRun {
		if needsMove {
			// Only when there would be a write. Rendering it for a
			// space already in place makes the preview unreadable:
			// moved=false means both "skipped" and "dry run", and the
			// body is the only thing that tells them apart.
			rendered, err := renderBody(body)
			if err != nil {
				return nil, err
			}
			out.Rendered = rendered
		}
		return out, nil
	}
	if !needsMove {
		return out, nil
	}

	moved, err := s.client.MoveSectionItem(ctx, item, body)
	if err != nil {
		return nil, Classify(err)
	}
	out.Moved = true
	// The item's resource name embeds its section, so the move renames
	// it and the name this call arrived with now answers 404. Returning
	// the stale one would be actively wrong: the tool tells callers to
	// feed item_name back in for a bulk sort.
	out.Item = movedItemName(moved, item, section)
	return out, nil
}

// locateSectionItem finds which section item stands for a space.
func (s *Service) locateSectionItem(ctx context.Context, space string) (string, error) {
	listed, err := s.client.ListSectionItems(ctx, gchat.ListSectionItemsOptions{Space: space, PageSize: 1})
	if err != nil {
		// The write scope, not the readonly one this lookup runs on.
		// Naming readonly would have the person grant it, get past this
		// call, and be refused by the move behind it.
		return "", needsScope(err, scopes.UserSections)
	}
	if len(listed.SectionItems) == 0 {
		return "", Failf(ClassNotFound,
			"No sidebar entry for %s. The space may not exist, or you may not be a member of it.", space)
	}
	item := listed.SectionItems[0]
	// This path trusts a server-side filter and what follows is a
	// write. If Google ever stops honouring that filter the first row
	// back is an arbitrary space, and this would move it.
	if item.Space != space {
		return "", Failf(ClassUpstream,
			"The lookup for %s returned a sidebar entry for %s. Nothing was moved.", space, orUnknown(item.Space))
	}
	if item.Name == "" || sectionOfItem(item.Name) == "" {
		return "", Failf(ClassUpstream, "The sidebar entry for %s has no resource name.", space)
	}
	return item.Name, nil
}

// movedItemName is the item's name after a move.
//
// Read defensively: the move has landed by the time this runs, so a
// response this server cannot parse must not become an error the caller
// would retry. The item id survives a move, so the fallback is exact.
func movedItemName(moved *gchat.MoveSectionItemResponse, item, section string) string {
	if moved != nil && moved.SectionItem != nil && moved.SectionItem.Name != "" {
		return moved.SectionItem.Name
	}
	_, id, found := strings.Cut(item, "/items/")
	if !found {
		return item
	}
	return section + "/items/" + id
}

// sameSection reports whether two section names mean the same section.
//
// The section id is compared, not the whole name, because the user
// segment has two spellings for one person: Google takes "users/me" on
// the way in and answers with "users/{id}". Comparing the strings would
// call every already-filed space a move, which is exactly the no-op
// this tool promises.
//
// Only the caller's own sections are addressable at all, so an id
// cannot collide across people; "me" therefore matches any user
// segment, and two different numeric ids do not.
func sameSection(a, b string) bool {
	aUser, aID, _ := strings.Cut(a, "/sections/")
	bUser, bID, _ := strings.Cut(b, "/sections/")
	if aID != bID {
		return false
	}
	return aUser == bUser || aUser == "users/me" || bUser == "users/me"
}

// orUnknown labels an empty value in a message to the caller.
func orUnknown(v string) string {
	if v == "" {
		return "an entry with no space"
	}
	return v
}
