// Package commands implements kubectl-topology's read subcommands (ADR-015 D-15.4), one file
// each. A command parses its flags with the standard library, asks the API through a client the
// caller supplies (Env.Client — the seam where the stored credential is attached), and prints
// either the shared renderer's lines or, with -o json, the response body unchanged.
package commands

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/auth"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/render"
)

// Exit codes (D-15.4). A script can tell "nothing observed" (0) from "could not ask" (2–4).
const (
	ExitOK         = 0 // success, including an empty result
	ExitUsage      = 1 // bad flags or arguments, or a name that needs narrowing
	ExitAuth       = 2 // not signed in, or the token was refused (401)
	ExitAPI        = 3 // any other API error (4xx/5xx)
	ExitConnection = 4 // the server could not be reached
)

// DefaultServer is the port-forwarded frontend of the kind demo.
const DefaultServer = "http://localhost:8080"

// ClientOptions are the connection flags every read command shares.
type ClientOptions struct {
	Server string
	CAFile string
}

// Env is everything a command touches outside itself, so tests can substitute each part.
type Env struct {
	Stdout, Stderr io.Writer
	Now            func() time.Time
	// Client returns an API client for the server, carrying that server's stored credential if
	// there is one (and none otherwise, D-15.13). A credential that exists but cannot be used is
	// returned as an *api.TokenError (exit 2); any other error is a usage error (bad URL, CA).
	Client func(ctx context.Context, o ClientOptions) (*api.Client, error)
	// MCPTransport is what `mcp` serves on; nil is stdio. Tests substitute an in-memory one.
	MCPTransport mcp.Transport
}

// Command is one read subcommand.
type Command struct {
	Name    string
	Summary string
	run     func(ctx context.Context, env Env, args []string) int
}

// Commands lists the read subcommands, and the MCP server that serves them, in help order.
var Commands = []Command{
	{"namespaces", "list namespaces observed in a window", runNamespaces},
	{"graph", "print every observed relationship in a window", runGraph},
	{"deps", "print what one workload talks to, and what talks to it", runDeps},
	{"diff", "compare two equal-length periods", runDiff},
	{"anomalies", "print rule-based anomalies in a window against the period before it", runAnomalies},
	{"edge", "print one relationship by its ID", runEdge},
	{"mcp", "serve these reads to an AI agent as MCP tools over stdio", runMCP},
}

// Run executes the read subcommand name with args and returns the process exit code. ok is
// false when name is not a read subcommand, so the caller can dispatch it elsewhere.
func Run(ctx context.Context, env Env, name string, args []string) (code int, ok bool) {
	for _, c := range Commands {
		if c.Name == name {
			if env.Now == nil {
				env.Now = time.Now
			}
			return c.run(ctx, env, args), true
		}
	}
	return ExitUsage, false
}

// common holds the flags every read command shares.
type common struct {
	client ClientOptions
	output string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.client.Server, "server", DefaultServer, "topology server URL (the frontend a browser would use)")
	fs.StringVar(&c.client.CAFile, "ca-file", "", "PEM CA for an https server with a private certificate")
	fs.StringVar(&c.output, "o", "text", "output format: text or json (the API response unchanged)")
}

// validate checks -o. text and json suit every command; extra names formats only some commands
// can produce (graph's mermaid, ADR-016 D-16.7), so asking another command for one is a usage
// error naming what it does accept rather than a silent fallback to text.
func (c *common) validate(extra ...string) error {
	allowed := append([]string{"text", "json"}, extra...)
	if !slices.Contains(allowed, c.output) {
		return fmt.Errorf("-o %q: want %s", c.output, orList(allowed))
	}
	return nil
}

// orList is "text or json", "text, json or mermaid".
func orList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

func newFlagSet(env Env, name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(env.Stderr, "usage: kubectl topology %s\n\nflags:\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses flags and positional arguments in any order, so "deps backend --since 1h" works
// as kubectl users expect; the standard flag package alone stops at the first positional.
// A bare "--" ends flag parsing.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if len(args) > 0 && len(rest) < len(args) && args[len(args)-len(rest)-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// usageError prints a usage error and returns its exit code. flag.ErrHelp (-h) is not an error.
func usageError(env Env, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
	return ExitUsage
}

// apiFailure reports an error from the client and maps it to an exit code (D-15.4, D-15.6).
func apiFailure(env Env, server string, err error) int {
	var (
		tokenErr *api.TokenError
		connErr  *api.ConnectionError
		statErr  *api.StatusError
	)
	var (
		notSignedIn *auth.NotSignedInError
		issuerDown  *auth.ConnectionError
	)
	switch {
	case errors.As(err, &notSignedIn):
		// Its message already names the server and the login command.
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", notSignedIn)
		return ExitAuth
	case errors.As(err, &issuerDown):
		// A refresh that could not be sent: the tokens are kept, and the next call succeeds once
		// the issuer is reachable again, so this is a connection failure, not a sign-out.
		_, _ = fmt.Fprintf(env.Stderr, "error: refreshing the sign-in: %v (is the port-forward to %s running?)\n",
			issuerDown, server)
		return ExitConnection
	case errors.As(err, &tokenErr):
		// The stored credential cannot be used (file permissions, lock timeout, a provider error
		// that is not a refusal). Signing in again would not fix any of these, so no login hint.
		_, _ = fmt.Fprintf(env.Stderr, "error: cannot use the stored sign-in for %s: %v\n", server, tokenErr.Err)
		return ExitAuth
	case api.IsUnauthorized(err):
		_, _ = fmt.Fprintf(env.Stderr, "error: %s refused the request: signed out, expired, or not admitted\n"+
			"run \"kubectl topology login --server %s\"\n", server, server)
		return ExitAuth
	case errors.As(err, &connErr):
		_, _ = fmt.Fprintf(env.Stderr, "error: %v (is the port-forward to %s running?)\n", connErr, server)
		return ExitConnection
	case errors.As(err, &statErr):
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", statErr)
		return ExitAPI
	default:
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
		return ExitAPI
	}
}

func connect(ctx context.Context, env Env, o ClientOptions) (*api.Client, int) {
	c, err := env.Client(ctx, o)
	var tokenErr *api.TokenError
	switch {
	case errors.As(err, &tokenErr):
		return nil, apiFailure(env, o.Server, err)
	case err != nil:
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
		return nil, ExitUsage
	}
	return c, ExitOK
}

// emit prints a successful response: the body unchanged for -o json, the rendered lines else.
func emit(env Env, output string, raw []byte, text render.Text) int {
	if output == "json" {
		if _, err := env.Stdout.Write(raw); err != nil {
			return ExitAPI
		}
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			_, _ = fmt.Fprintln(env.Stdout)
		}
		return ExitOK
	}
	_, _ = fmt.Fprint(env.Stdout, text.String())
	return ExitOK
}

// multi is a repeatable string flag (-n a -n b), also accepting comma-separated values.
type multi []string

func (m *multi) String() string { return strings.Join(*m, ",") }

func (m *multi) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*m = append(*m, part)
		}
	}
	return nil
}
