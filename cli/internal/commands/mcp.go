package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/auth"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/mcpserver"
)

// Version is reported by `version` and in the MCP handshake; main sets it from -ldflags.
var Version = "dev"

// runMCP serves the read tools over stdio to the agent that launched it (D-15.5). Stdout carries
// only the protocol from here on: every message, flag errors included, goes to stderr. It never
// opens a browser: a missing sign-in is a tool error naming the login command.
func runMCP(ctx context.Context, env Env, args []string) int {
	var o ClientOptions
	fs := newFlagSet(env, "mcp", "mcp [--server URL] [--ca-file FILE]")
	fs.StringVar(&o.Server, "server", DefaultServer, "the saved server this process reads; one process serves one server")
	fs.StringVar(&o.CAFile, "ca-file", "", "PEM CA for an https server with a private certificate")
	pos, err := parse(fs, args)
	if err != nil {
		return usageError(env, err)
	}
	if len(pos) > 0 {
		return usageError(env, errors.New("mcp takes no arguments"))
	}
	server, err := auth.NormalizeServer(o.Server)
	if err != nil {
		return usageError(env, err)
	}
	cfg := mcpserver.Config{
		Server:  server,
		Now:     env.Now,
		Version: Version,
		Log:     env.Stderr,
		// Called on every tool call, so the credential file is read afresh each time.
		Client: func(ctx context.Context) (*api.Client, error) { return env.Client(ctx, o) },
	}
	transport := env.MCPTransport
	if transport == nil {
		transport = &mcp.StdioTransport{}
	}
	if err := mcpserver.Serve(ctx, cfg, transport); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(env.Stderr, "error: mcp: %v\n", err)
		return ExitAPI
	}
	return ExitOK
}
