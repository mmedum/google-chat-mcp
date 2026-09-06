// Command google-chat-mcp exposes Google Chat to MCP clients over stdio.
//
// It speaks MCP on standard input and output, so stdout carries nothing
// but JSON-RPC frames. Every log line goes to stderr, and the CLI
// subcommands print to stdout only because they are not the server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/server"
	"github.com/mmedum/google-chat-mcp/internal/version"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `google-chat-mcp — Google Chat as MCP tools.

Usage:
  google-chat-mcp                          serve MCP over stdio
  google-chat-mcp login --client-secret P  authorize a Google account
  google-chat-mcp logout                   revoke and delete the stored token
  google-chat-mcp status                   account, token location, settings
  google-chat-mcp doctor [--spaces N]      check live responses against our models
  google-chat-mcp --version
  google-chat-mcp --dump-schemas           tool and resource schemas as JSON

Settings come from GCM_* environment variables; every subcommand also
accepts the matching flags (run one with -h).
`)
}

// run dispatches a subcommand. It returns the process exit code so the
// whole command line is testable without spawning anything.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			usage(stdout)
			return 0
		case "--version", "-version":
			_, _ = fmt.Fprintln(stdout, version.Info())
			return 0
		case "--dump-schemas":
			return cmdDumpSchemas(args[1:], stdout, stderr)
		case "login":
			return cmdLogin(args[1:], stdout, stderr)
		case "logout":
			return cmdLogout(args[1:], stdout, stderr)
		case "status":
			return cmdStatus(args[1:], stdout, stderr)
		case "doctor":
			return cmdDoctor(args[1:], stdout, stderr)
		}
		// Anything else that is not a flag was meant to be a
		// subcommand. Falling through would start the server instead,
		// which looks like a hang: it blocks on stdin and says nothing.
		if !strings.HasPrefix(args[0], "-") {
			usage(stderr)
			return fail(stderr, "unknown command %q", args[0])
		}
	}
	return cmdServe(args, stderr)
}

// fail prints a message to stderr and returns the exit code to use.
func fail(stderr io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(stderr, "error: "+format+"\n", args...)
	return 1
}

// loadConfig parses flags and the environment into a validated Config.
// extra registers any flags the subcommand adds beyond the settings.
func loadConfig(name string, args []string, stderr io.Writer, extra func(*flag.FlagSet)) (config.Config, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	settings := config.Define(fs, os.Getenv)
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	return settings.Build()
}

// cmdServe runs the MCP server on stdio until stdin closes.
//
// Nothing here writes to stdout. A single stray line would break the
// JSON-RPC framing before the client's first request completes, and the
// failure would look like the server never started.
func cmdServe(args []string, stderr io.Writer) int {
	cfg, err := loadConfig("serve", args, stderr, nil)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	log := config.NewLogger(cfg, stderr)

	// The server keeps running when credentials are missing or stale.
	// A client that cannot start a server shows "failed to connect" and
	// the person never sees why; a server that starts and answers with
	// an [auth] error tells them exactly what to run.
	deps, err := build(context.Background(), cfg, log)
	if err != nil {
		log.Error("startup", "error", err)
		return fail(stderr, "%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("serving MCP over stdio",
		"version", version.String(),
		"read_only", cfg.ReadOnly,
		"toolsets", cfg.Toolsets)

	s := server.New(server.Deps{
		Service: deps.Service, Config: cfg, Logger: log, Version: version.String(),
	})
	// A closed stdin is how a client says it is done, and the spec makes
	// that the graceful shutdown signal. It arrives as an EOF, so it is
	// an ordinary exit rather than a failure to report.
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil && !isShutdown(err) {
		log.Error("serve", "error", err)
		return 1
	}
	return 0
}

// isShutdown reports whether err is the client going away rather than
// something going wrong.
func isShutdown(err error) bool {
	switch {
	case errors.Is(err, context.Canceled),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, os.ErrClosed):
		return true
	}
	// The SDK answers a closed connection with a JSON-RPC error and
	// folds the EOF into its message rather than wrapping it, so
	// errors.Is finds nothing above. The code is the half that does not
	// move: -32004 "server is closing" and -32003 "client is closing".
	// They live in the SDK's internal jsonrpc2 and are still reachable,
	// because the public jsonrpc package aliases that type rather than
	// redefining it. Matching the message text instead reads the same
	// and breaks silently on a wording change, and the cost of getting
	// this wrong is every host recording an ordinary disconnect as a
	// crash.
	var wire *jsonrpc.Error
	if errors.As(err, &wire) {
		return wire.Code == codeServerClosing || wire.Code == codeClientClosing
	}
	return false
}

// The SDK exports neither value. Read off jsonrpc2.ErrServerClosing and
// ErrClientClosing at go-sdk v1.7.0.
const (
	codeServerClosing = -32004
	codeClientClosing = -32003
)

// cmdDumpSchemas prints the tool and resource schemas as JSON. It needs
// no credentials: registration does not call Google.
func cmdDumpSchemas(args []string, stdout, stderr io.Writer) int {
	cfg, err := loadConfig("--dump-schemas", args, stderr, nil)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	log := config.NewLogger(cfg, io.Discard)
	deps, err := build(context.Background(), cfg, log)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	s := server.New(server.Deps{
		Service: deps.Service, Config: cfg, Logger: log, Version: version.String(),
	})
	if err := server.DumpSchemas(context.Background(), s, stdout, version.String()); err != nil {
		return fail(stderr, "%v", err)
	}
	return 0
}
