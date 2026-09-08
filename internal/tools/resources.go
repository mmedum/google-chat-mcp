package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

// resourceScheme is the URI scheme of this server's resources.
const resourceScheme = "gchat://"

// registerResources adds the three resource templates.
//
// Each carries the same content as the matching get_ tool. They exist
// for clients that attach a space or a message to a conversation whole,
// rather than calling a tool; the ids in the URI are bare, because that
// is what a person copies out of a Chat URL.
func registerResources(s *mcp.Server, d Deps) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "space",
		Title:       "Google Chat space",
		URITemplate: resourceScheme + "spaces/{space_id}",
		MIMEType:    "application/json",
		Description: "A single Google Chat space by id. Same content as the get_space tool. " +
			"space_id is the bare id, with no spaces/ prefix.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		parts, err := resourceParts(req.Params.URI, "")
		if err != nil {
			return nil, err
		}
		got, err := d.Service.GetSpace(ctx, parts.space)
		if err != nil {
			return nil, resourceError(req.Params.URI, err)
		}
		return jsonResource(req.Params.URI, spaceDetail(got))
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "message",
		Title:       "Google Chat message",
		URITemplate: resourceScheme + "spaces/{space_id}/messages/{message_id}",
		MIMEType:    "application/json",
		Description: "A single Google Chat message by id, with its reaction summaries inline. Same content as the " +
			"get_message tool. Both ids are bare, with no prefix.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		parts, err := resourceParts(req.Params.URI, "messages")
		if err != nil {
			return nil, err
		}
		got, err := d.Service.GetMessage(ctx, parts.child)
		if err != nil {
			return nil, resourceError(req.Params.URI, err)
		}
		return jsonResource(req.Params.URI, messageDetail(got))
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "thread",
		Title:       "Google Chat thread",
		URITemplate: resourceScheme + "spaces/{space_id}/threads/{thread_id}",
		MIMEType:    "application/json",
		Description: "One thread's messages, oldest first, up to 100 of them. Same content as the get_thread " +
			"tool. Both ids are bare, with no prefix. A thread longer than 100 messages needs get_thread, " +
			"which pages.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		parts, err := resourceParts(req.Params.URI, "threads")
		if err != nil {
			return nil, err
		}
		// A resource cannot page, so it asks for as much as the tool
		// allows rather than the tool's smaller default. Past that the
		// caller needs get_thread, and the description says so.
		got, err := d.Service.GetThread(ctx, service.GetThreadInput{
			Space: parts.space, Thread: parts.child, Limit: service.MaxLimit,
		})
		if err != nil {
			return nil, resourceError(req.Params.URI, err)
		}
		// The token is carried even here, where the resource cannot pass
		// one back: a reader that sees it knows the thread is longer
		// than this, which is the difference between a prefix and a
		// thread.
		return jsonResource(req.Params.URI, MessageListOutput{
			Result:        messageRows(got.Messages),
			NextPageToken: nullable(got.NextPageToken),
		})
	})
}

// uriParts is a resource URI split into the names the service wants.
type uriParts struct {
	// space is "spaces/{id}".
	space string
	// child is "spaces/{id}/{kind}/{id}", empty for the space template.
	child string
}

// resourceParts turns a gchat:// URI into resource names.
//
// The template already matched, so anything malformed here is a URI
// that does not name a resource: the spec's not-found, not an invalid
// argument. Both a bare id and a full resource name are accepted,
// because a model that has seen "spaces/AAA" tends to send it back.
func resourceParts(uri, childKind string) (uriParts, error) {
	rest, ok := strings.CutPrefix(uri, resourceScheme+"spaces/")
	if !ok {
		return uriParts{}, mcp.ResourceNotFoundError(uri)
	}
	spaceID, childID, _ := strings.Cut(rest, "/")
	spaceID, err := url.PathUnescape(spaceID)
	if err != nil || spaceID == "" {
		return uriParts{}, mcp.ResourceNotFoundError(uri)
	}
	parts := uriParts{space: "spaces/" + spaceID}
	if childKind == "" {
		if childID != "" {
			return uriParts{}, mcp.ResourceNotFoundError(uri)
		}
		return parts, nil
	}
	childID, ok = strings.CutPrefix(childID, childKind+"/")
	if !ok {
		return uriParts{}, mcp.ResourceNotFoundError(uri)
	}
	if childID, err = url.PathUnescape(childID); err != nil || childID == "" {
		return uriParts{}, mcp.ResourceNotFoundError(uri)
	}
	parts.child = fmt.Sprintf("%s/%s/%s", parts.space, childKind, childID)
	return parts, nil
}

// resourceError maps a service failure onto the protocol. A space or
// message that is not there is the spec's resource-not-found; anything
// else keeps the "[class] message" a model can act on.
func resourceError(uri string, err error) error {
	var se *service.Error
	if errors.As(err, &se) && (se.Class == service.ClassNotFound || se.Class == service.ClassInvalid) {
		return mcp.ResourceNotFoundError(uri)
	}
	return fail(err)
}

// jsonResource encodes a value as the resource's content.
func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fail(err)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: uri, MIMEType: "application/json", Text: string(body),
	}}}, nil
}
