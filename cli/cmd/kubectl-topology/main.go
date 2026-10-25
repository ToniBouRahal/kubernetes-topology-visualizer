// Command kubectl-topology is the topology visualizer's command-line client and MCP server
// (ADR-015). On PATH, kubectl's plugin discovery makes it `kubectl topology`. This file only
// dispatches: the read commands and `mcp` live in internal/commands, sign-in in internal/auth.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/auth"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/commands"
)

// version is set at build time: -ldflags "-X main.version=$(git describe)".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	commands.Version = version
	if len(args) == 0 {
		usage(stderr)
		return commands.ExitUsage
	}
	name, rest := args[0], args[1:]
	switch name {
	case "login":
		return auth.Login(ctx, rest, stdout, stderr)
	case "logout":
		return auth.Logout(ctx, rest, stdout, stderr)
	case "status":
		return auth.Status(ctx, rest, stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, version)
		return commands.ExitOK
	case "help", "-h", "--help":
		usage(stdout)
		return commands.ExitOK
	}
	env := commands.Env{Stdout: stdout, Stderr: stderr, Now: time.Now, Client: client}
	if code, ok := commands.Run(ctx, env, name, rest); ok {
		return code
	}
	fmt.Fprintf(stderr, "error: unknown command %q\n\n", name)
	usage(stderr)
	return commands.ExitUsage
}

// client attaches the server's stored credential, if any, to an API client. It is called once per
// command and once per MCP tool call; the token source itself re-reads the credentials file on
// every request and never keeps a token.
func client(_ context.Context, o commands.ClientOptions) (*api.Client, error) {
	httpClient, tokens, err := auth.ClientFor(o.Server, o.CAFile)
	if err != nil {
		return nil, err
	}
	return api.New(tokens.Server, httpClient, tokens)
}

func usage(w io.Writer) {
	fmt.Fprint(w, "usage: kubectl topology COMMAND [flags]\n\ncommands:\n")
	for _, c := range commands.Commands {
		fmt.Fprintf(w, "  %-11s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintf(w, "  %-11s %s\n", "login", "sign in through the browser (OIDC, PKCE)")
	fmt.Fprintf(w, "  %-11s %s\n", "logout", "forget a server's stored sign-in")
	fmt.Fprintf(w, "  %-11s %s\n", "status", "show who is signed in to which server, and until when")
	fmt.Fprintf(w, "  %-11s %s\n", "version", "print the version")
	fmt.Fprint(w, "\nRun \"kubectl topology COMMAND -h\" for a command's flags.\n")
}
