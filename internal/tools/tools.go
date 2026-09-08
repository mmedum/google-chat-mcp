// Package tools registers the MCP tools. A handler validates its input,
// calls the service and shapes the result. Every rule worth testing
// lives in the service.
package tools

import (
	"errors"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

// Deps are what the tools need.
type Deps struct {
	Service *service.Service
	Config  config.Config
	Logger  *slog.Logger
}

// Register adds every tool the configuration allows.
func Register(s *mcp.Server, d Deps) {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	registerSpaces(s, d)
	registerSpaceState(s, d)
	registerPresence(s, d)
	registerSpaceWrites(s, d)
	registerMessages(s, d)
	registerMessageWrites(s, d)
	registerMembers(s, d)
	registerMemberWrites(s, d)
	registerReactions(s, d)
	registerReactionWrites(s, d)
	registerSections(s, d)
	registerSectionWrites(s, d)
	registerPeople(s, d)
	registerAttachments(s, d)
	registerEvents(s, d)
	registerResources(s, d)
}

// fail converts a service error into the text the model sees. The SDK
// turns a returned error into a result with isError set, which is what
// the spec wants for an upstream refusal; a JSON-RPC error is reserved
// for the caller getting the protocol wrong.
func fail(err error) error {
	var se *service.Error
	if errors.As(err, &se) {
		return errors.New(se.Error())
	}
	return errors.New("[" + string(service.ClassUnexpected) + "] " + err.Error())
}
