package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

// SectionOutput is one group in the caller's sidebar.
type SectionOutput struct {
	SectionName string `json:"section_name" jsonschema:"the section's resource name, users/{user}/sections/{section}"`
	DisplayName string `json:"display_name" jsonschema:"what to call it. Google names only custom sections, so a system one gets a parenthesised label here"`
	Type        string `json:"type" jsonschema:"CUSTOM_SECTION, DEFAULT_DIRECT_MESSAGES, DEFAULT_SPACES, DEFAULT_APPS, or SECTION_TYPE_UNSPECIFIED"`
	SortOrder   *int   `json:"sort_order" jsonschema:"where it sits in the sidebar; set by position_section and null when Google gave no rank"`
}

// ListSectionsOutput is a page of sections.
type ListSectionsOutput struct {
	Sections      []SectionOutput `json:"sections" jsonschema:"the caller's own sidebar sections"`
	NextPageToken *string         `json:"next_page_token" jsonschema:"non-null means limit cut the listing short and more sections exist; pass it back as page_token"`
	Unparsed      int             `json:"unparsed" jsonschema:"rows this listing could not read and skipped. Non-zero means the result is INCOMPLETE and this server's models are stale, not that the sidebar is that small"`
}

// ListSectionsInput selects a page of the caller's sections.
type ListSectionsInput struct {
	Limit     int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100; default 50"`
	PageToken string `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

// SectionItemOutput is one space filed under a section.
type SectionItemOutput struct {
	ItemName    string  `json:"item_name" jsonschema:"the item's resource name; move_space_to_section takes it"`
	SectionName string  `json:"section_name" jsonschema:"the section this item currently sits in"`
	SpaceID     *string `json:"space_id" jsonschema:"the space filed here. Null means a kind of item this server does not model yet: Google's item field is a union and only spaces are members of it today"`
}

// ListSectionItemsOutput is a page of section items.
type ListSectionItemsOutput struct {
	Items         []SectionItemOutput `json:"items" jsonschema:"the spaces filed under the section"`
	NextPageToken *string             `json:"next_page_token" jsonschema:"non-null means limit cut the listing short. A bulk re-sort built on a truncated listing skips the spaces it never saw, so page rather than assume"`
	Unparsed      int                 `json:"unparsed" jsonschema:"rows this listing could not read and skipped. Non-zero means the result is INCOMPLETE, not that the section is that small"`
}

// ListSectionItemsInput selects which items to list.
type ListSectionItemsInput struct {
	SectionName string `json:"section_name,omitempty" jsonschema:"read one section, users/{user}/sections/{section}"`
	SpaceID     string `json:"space_id,omitempty" jsonschema:"find which section this space sits in, spaces/{id}"`
	Limit       int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100; default 50"`
	PageToken   string `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

func registerSections(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "list_sections",
		Description: "List the sections in the caller's own Chat sidebar: the system ones ('(direct messages)', " +
			"'(spaces)', '(apps)') plus any custom ones they made. Sections are per-user and change nobody else's " +
			"view. A non-null next_page_token means more sections exist beyond limit. Needs the " +
			"chat.users.sections.readonly scope.",
		Kind:    Read,
		Toolset: config.ToolsetSections,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListSectionsInput) (*mcp.CallToolResult, ListSectionsOutput, error) {
		got, err := d.Service.ListSections(ctx, service.ListSectionsInput{Limit: in.Limit, PageToken: in.PageToken})
		if err != nil {
			return nil, ListSectionsOutput{}, err
		}
		out := ListSectionsOutput{
			Sections:      make([]SectionOutput, 0, len(got.Sections)),
			NextPageToken: nullable(got.NextPageToken),
			Unparsed:      got.Unparsed,
		}
		for _, sec := range got.Sections {
			out.Sections = append(out.Sections, SectionOutput{
				SectionName: sec.Name,
				DisplayName: sec.DisplayName,
				Type:        sec.Type,
				SortOrder:   sec.SortOrder,
			})
		}
		return nil, out, nil
	})

	register(s, d, spec{
		Name: "list_section_items",
		Description: "List the spaces filed under a section. Pass section_name to read one section, space_id to find " +
			"which section a single space sits in, or both to check one against the other; at least one is required. " +
			"A non-null next_page_token means limit cut the listing short, so page rather than treat it as the whole " +
			"section. Needs the chat.users.sections.readonly scope.",
		Kind:    Read,
		Toolset: config.ToolsetSections,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListSectionItemsInput) (*mcp.CallToolResult, ListSectionItemsOutput, error) {
		got, err := d.Service.ListSectionItems(ctx, service.ListSectionItemsInput{
			Section: in.SectionName, Space: in.SpaceID, Limit: in.Limit, PageToken: in.PageToken,
		})
		if err != nil {
			return nil, ListSectionItemsOutput{}, err
		}
		out := ListSectionItemsOutput{
			Items:         make([]SectionItemOutput, 0, len(got.Items)),
			NextPageToken: nullable(got.NextPageToken),
			Unparsed:      got.Unparsed,
		}
		for _, item := range got.Items {
			out.Items = append(out.Items, SectionItemOutput{
				ItemName:    item.Name,
				SectionName: item.SectionName,
				SpaceID:     nullable(item.Space),
			})
		}
		return nil, out, nil
	})
}

// CreateSectionInput is a new custom section.
type CreateSectionInput struct {
	DisplayName string `json:"display_name" jsonschema:"what to call it, 1 to 80 characters"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"return the request body without creating anything"`
}

// SectionWriteOutput is the section a create or rename produced.
type SectionWriteOutput struct {
	SectionName     *string          `json:"section_name" jsonschema:"the section's resource name; null after a dry-run create, because Google assigns the id"`
	DisplayName     string           `json:"display_name" jsonschema:"what it is called"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null otherwise"`
}

// RenameSectionInput is a new label for one section.
type RenameSectionInput struct {
	SectionName string `json:"section_name" jsonschema:"the section to rename, users/{user}/sections/{section}, from list_sections"`
	DisplayName string `json:"display_name" jsonschema:"the new label, 1 to 80 characters"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"return the patch body without applying it"`
}

// DeleteSectionInput names a section to delete.
type DeleteSectionInput struct {
	SectionName string `json:"section_name" jsonschema:"the section to delete, users/{user}/sections/{section}"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without deleting anything"`
}

// DeleteSectionOutput says whether anything was deleted.
type DeleteSectionOutput struct {
	SectionName string `json:"section_name" jsonschema:"the section that was named"`
	Deleted     bool   `json:"deleted" jsonschema:"false when the section was already gone, and after a dry run; neither is an error. A refusal is reported as an error rather than as false"`
	DryRun      bool   `json:"dry_run" jsonschema:"true when nothing was deleted"`
}

// PositionSectionInput moves a section in the sidebar.
type PositionSectionInput struct {
	SectionName      string `json:"section_name" jsonschema:"the section to move, users/{user}/sections/{section}"`
	SortOrder        *int   `json:"sort_order,omitempty" jsonschema:"an absolute rank, 1 and up; pass this or relative_position, not both"`
	RelativePosition string `json:"relative_position,omitempty" jsonschema:"START or END; pass this or sort_order, not both"`
	DryRun           bool   `json:"dry_run,omitempty" jsonschema:"return the request body without moving anything"`
}

// PositionSectionOutput is where the section ended up.
type PositionSectionOutput struct {
	SectionName     string           `json:"section_name" jsonschema:"the section that was moved"`
	SortOrder       *int             `json:"sort_order" jsonschema:"the rank Google reports after the move, when it reports one; null after a dry run"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was moved"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null on a real move"`
}

// MoveSpaceToSectionInput files one space under one section.
type MoveSpaceToSectionInput struct {
	SpaceID     string `json:"space_id" jsonschema:"the space to file, spaces/{id}"`
	SectionName string `json:"section_name" jsonschema:"where to file it, users/{user}/sections/{section}"`
	ItemName    string `json:"item_name,omitempty" jsonschema:"the space's current sidebar entry, from list_section_items. Passing it skips a lookup, which is what makes a bulk sort cost one listing per section instead of a request per space. Nothing re-checks that it belongs to space_id, so a mismatched pair files the wrong space"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"look up where the space sits without moving it"`
}

// MoveSpaceToSectionOutput says where the space was and where it went.
type MoveSpaceToSectionOutput struct {
	SpaceID     string `json:"space_id" jsonschema:"the space that was filed"`
	SectionName string `json:"section_name" jsonschema:"the section it was asked to go to"`
	ItemName    string `json:"item_name" jsonschema:"the sidebar entry after the move; feed it back in for a bulk sort. A move renames it, so the one you passed in no longer exists"`
	FromSection string `json:"from_section" jsonschema:"where the space sat before, a default section when it had never been filed. Resolved before anything is written, so a dry run reports it too"`
	Moved       bool   `json:"moved" jsonschema:"false without an error in two cases: a dry run, and a space already in the target section"`
	DryRun      bool   `json:"dry_run" jsonschema:"true when nothing was moved"`
	// Null on an already-filed space, which is how a caller tells that
	// apart from a dry run: both report moved false.
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run that would move something, the exact body; null on a real move and on a space already in place"`
}

func registerSectionWrites(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "create_section",
		Description: "Add a custom section to your own Chat sidebar. Google does not merge sections by name, so two " +
			"calls leave two sections with the same label: list_sections first if you mean to reuse one. Sections are " +
			"per-user and change nobody else's view. Set dry_run to see the request body first. Needs the " +
			"chat.users.sections scope.",
		Kind:    Write,
		Toolset: config.ToolsetSections,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateSectionInput) (*mcp.CallToolResult, SectionWriteOutput, error) {
		got, err := d.Service.CreateSection(ctx, service.CreateSectionInput{
			DisplayName: in.DisplayName, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, SectionWriteOutput{}, err
		}
		return nil, sectionWritten(got), nil
	})

	register(s, d, spec{
		Name: "rename_section",
		Description: "Change a custom section's label. Only custom sections can be renamed; Google refuses a patch " +
			"against a system one. Set dry_run to see the patch body first. Needs the chat.users.sections scope.",
		Kind:    WriteIdempotent,
		Toolset: config.ToolsetSections,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RenameSectionInput) (*mcp.CallToolResult, SectionWriteOutput, error) {
		got, err := d.Service.RenameSection(ctx, service.RenameSectionInput{
			Section: in.SectionName, DisplayName: in.DisplayName, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, SectionWriteOutput{}, err
		}
		return nil, sectionWritten(got), nil
	})

	register(s, d, spec{
		Name: "delete_section",
		Description: "Delete a custom section from your own sidebar. The spaces in it are not deleted: Google files " +
			"them back under the default sections. A section that is already gone reports deleted false rather than " +
			"failing. A refusal is reported, because that is Google declining to delete a system section. Needs the " +
			"chat.users.sections scope.",
		Kind:    Destructive,
		Toolset: config.ToolsetSections,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteSectionInput) (*mcp.CallToolResult, DeleteSectionOutput, error) {
		got, err := d.Service.DeleteSection(ctx, service.DeleteSectionInput{
			Section: in.SectionName, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, DeleteSectionOutput{}, err
		}
		return nil, DeleteSectionOutput{SectionName: got.Name, Deleted: got.Deleted, DryRun: got.DryRun}, nil
	})

	register(s, d, spec{
		Name: "position_section",
		Description: "Move a section within your sidebar. Pass exactly one of sort_order (absolute, 1 and up) or " +
			"relative_position (START or END). Prefer relative_position when arranging several: Google documents " +
			"sort_order as inserting at that rank and shifting the rest down, but a run of absolute inserts does not " +
			"settle where that implies, so build an order by moving each to START in reverse. Set dry_run to see the " +
			"request body first. Needs the chat.users.sections scope.",
		Kind:    WriteIdempotent,
		Toolset: config.ToolsetSections,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PositionSectionInput) (*mcp.CallToolResult, PositionSectionOutput, error) {
		got, err := d.Service.PositionSection(ctx, service.PositionSectionInput{
			Section: in.SectionName, SortOrder: in.SortOrder, Relative: in.RelativePosition, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, PositionSectionOutput{}, err
		}
		return nil, PositionSectionOutput{
			SectionName:     got.Name,
			SortOrder:       got.SortOrder,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "move_space_to_section",
		Description: "File a space under a section in your own sidebar. Every space already sits in some section, a " +
			"default one until you move it, so this both files and re-files, and reports moved false when the space " +
			"is already where you want it. For a bulk sort, read each section once with list_section_items and pass " +
			"the item_name it gives you: that skips the per-space lookup. Set dry_run to see which section the space " +
			"would leave without writing. Needs the chat.users.sections scope.",
		Kind:    WriteIdempotent,
		Toolset: config.ToolsetSections,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in MoveSpaceToSectionInput) (*mcp.CallToolResult, MoveSpaceToSectionOutput, error) {
		got, err := d.Service.MoveSpaceToSection(ctx, service.MoveSpaceToSectionInput{
			Space: in.SpaceID, Section: in.SectionName, Item: in.ItemName, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, MoveSpaceToSectionOutput{}, err
		}
		return nil, MoveSpaceToSectionOutput{
			SpaceID:         got.Space,
			SectionName:     got.Section,
			ItemName:        got.Item,
			FromSection:     got.From,
			Moved:           got.Moved,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})
}

// sectionWritten shapes a create or a rename for the model. The two
// answer in one shape because they differ only in what they take.
func sectionWritten(got *service.SectionWriteResult) SectionWriteOutput {
	return SectionWriteOutput{
		SectionName:     nullable(got.Name),
		DisplayName:     got.DisplayName,
		DryRun:          got.DryRun,
		RenderedPayload: rendered(got.Rendered),
	}
}
