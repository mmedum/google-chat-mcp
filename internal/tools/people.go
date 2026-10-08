package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v5/internal/service"
)

// PersonHitOutput is one match from a people search.
type PersonHitOutput struct {
	UserID      *string `json:"user_id" jsonschema:"the Chat user id, users/{id}, when the hit is a Workspace profile. Null for a personal contact, whose id does not name anyone in Chat"`
	Email       *string `json:"email" jsonschema:"their email address, or null when the hit carried none"`
	DisplayName *string `json:"display_name" jsonschema:"what to call them, or null when the hit carried no name"`
	Source      string  `json:"source" jsonschema:"DIRECTORY for a Workspace profile, CONTACTS for the caller's own contacts"`
}

// SearchPeopleOutput is what a lookup found.
type SearchPeopleOutput struct {
	People           []PersonHitOutput `json:"people" jsonschema:"the matches, deduplicated across sources"`
	TotalReturned    int               `json:"total_returned" jsonschema:"how many matches are in people"`
	SourcesAttempted []string          `json:"sources_attempted" jsonschema:"which upstreams were asked"`
	SourcesSucceeded []string          `json:"sources_succeeded" jsonschema:"which upstreams answered. A source that was attempted but did not succeed refused, and the matches come from the rest"`
}

// SearchPeopleInput is a lookup by name or address fragment.
type SearchPeopleInput struct {
	Query   string   `json:"query" jsonschema:"a name or email fragment, 1 to 200 characters"`
	Limit   int      `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100; default 10"`
	Sources []string `json:"sources,omitempty" jsonschema:"which upstreams to ask: DIRECTORY for the Workspace directory, CONTACTS for the caller's own contacts. Default asks both, which covers a Workspace account and a personal one"`
}

// FindGroupInput is a Google Group's email address.
type FindGroupInput struct {
	Email string `json:"email" jsonschema:"the group's email address, such as team@example.com"`
}

// FindGroupOutput is the group that address names.
type FindGroupOutput struct {
	GroupName   string  `json:"group_name" jsonschema:"the group's resource name, groups/{id}; pass it to add_member as group_name"`
	Email       string  `json:"email" jsonschema:"the address that was looked up"`
	DisplayName *string `json:"display_name" jsonschema:"the group's name, or null when Cloud Identity gave none"`
}

func registerPeople(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "find_group",
		Description: "Turn a Google Group's email address into its groups/{id} name, which add_member takes as " +
			"group_name. Chat names a group only by that id. Looked up through Cloud Identity, which shows only " +
			"the groups this account can see. Google answers an address no group has the same way as a group kept " +
			"to its own members, so a refusal cannot say which.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FindGroupInput) (*mcp.CallToolResult, FindGroupOutput, error) {
		got, err := d.Service.FindGroup(ctx, in.Email)
		if err != nil {
			return nil, FindGroupOutput{}, err
		}
		return nil, FindGroupOutput{
			GroupName:   got.Name,
			Email:       got.Email,
			DisplayName: nullable(got.DisplayName),
		}, nil
	})

	register(s, d, spec{
		Name: "search_people",
		Description: "Look someone up across the caller's Workspace directory and their own contacts, and get their " +
			"email address. Each hit says which source produced it. Directory hits are remembered, so a later " +
			"get_messages or list_members resolves the same person without another lookup. Use it to turn a name " +
			"or a fragment of one into the email address the other tools take.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchPeopleInput) (*mcp.CallToolResult, SearchPeopleOutput, error) {
		sources := make([]service.PeopleSource, 0, len(in.Sources))
		for _, s := range in.Sources {
			sources = append(sources, service.PeopleSource(s))
		}
		got, err := d.Service.SearchPeople(ctx, service.SearchPeopleInput{
			Query: in.Query, Limit: in.Limit, Sources: sources,
		})
		if err != nil {
			return nil, SearchPeopleOutput{}, err
		}
		out := SearchPeopleOutput{
			People:           make([]PersonHitOutput, 0, len(got.People)),
			TotalReturned:    len(got.People),
			SourcesAttempted: sourceNames(got.Attempted),
			SourcesSucceeded: sourceNames(got.Succeeded),
		}
		for _, p := range got.People {
			out.People = append(out.People, PersonHitOutput{
				UserID:      nullable(p.UserID),
				Email:       nullable(p.Email),
				DisplayName: nullable(p.DisplayName),
				Source:      string(p.Source),
			})
		}
		return nil, out, nil
	})
}

func sourceNames(in []service.PeopleSource) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}
