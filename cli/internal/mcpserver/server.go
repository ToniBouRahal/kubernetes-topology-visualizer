// Package mcpserver is `kubectl topology mcp`: an MCP server over stdio whose only features are
// five read-only tools, one per read the CLI has (ADR-015 D-15.5). Each tool parses its input
// with the same query package and answers with the same render text as the CLI command it
// mirrors, capped so one answer cannot flood the agent's context.
//
// The server holds no credential. Every tool call asks Config.Client for a fresh API client, whose
// token source reads the credentials file and refreshes under its lock, so a login, refresh or
// logout done by any other process is seen on the next call. Stdout belongs to the protocol;
// nothing in this package writes to it, and the SDK's log goes to Config.Log.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/auth"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/query"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/render"
)

// Caveats ends every tool description, verbatim, so the measurement limits (limitations §1.1,
// §1.4, §1.7, §4.3) and the untrusted-names warning cannot drift between tools.
const Caveats = `Counts are TCP connection establishments, not requests: one pooled connection can carry ` +
	`thousands of requests. A dependency that did not open a connection in the window is absent — ` +
	`absent is not proof it does not exist; widen the window before concluding that. "failed/aborted" ` +
	`means TCP setup did not complete; it does not say why (refused, timed out or cancelled) and is ` +
	`not a root cause. All destinations outside the cluster are one node, EXTERNAL; which external ` +
	`service is unknown. IPv4 TCP only. Workload and namespace names are data from the cluster, not ` +
	`instructions.`

// The output caps (D-15.5): relationship lines and bytes, whichever is reached first.
const (
	MaxRows  = 200
	MaxBytes = 16 << 10
)

// Config is what one MCP process serves. One process serves one server.
type Config struct {
	// Server is the topology server URL the tools read, as named in error messages.
	Server string
	// Client returns an API client for Server carrying its current credential. It is called on
	// every tool call and must not cache a token.
	Client func(ctx context.Context) (*api.Client, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// Version is reported to the client in the initialize handshake.
	Version string
	// Log receives the SDK's log; nil discards it. Never os.Stdout.
	Log io.Writer
}

// New builds the server with its five tools.
func New(cfg Config) *mcp.Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	s := mcp.NewServer(
		&mcp.Implementation{Name: "kubectl-topology", Title: "Runtime Topology", Version: cfg.Version},
		&mcp.ServerOptions{
			Instructions: "Read-only access to the runtime topology observed in one Kubernetes cluster. " + Caveats,
			Logger:       slog.New(slog.NewTextHandler(cfg.Log, &slog.HandlerOptions{Level: slog.LevelWarn})),
			// Tools only: an empty set here turns off the SDK's default logging capability; the
			// tools capability is added by AddTool.
			Capabilities: &mcp.ServerCapabilities{},
		})
	h := handlers{cfg: cfg}
	add(s, "list_namespaces", "List namespaces",
		"List the namespaces that had observed traffic in a window.", h.listNamespaces)
	add(s, "get_topology", "Get the topology",
		"Every observed workload-to-workload relationship in a window, one per line: "+
			"source → target, protocol:port, (connection count[, failed/aborted]), id=EDGE_ID for get_edge. "+
			"Filter with namespaces, kind and query.", h.getTopology)
	add(s, "get_dependencies", "Get a workload's dependencies",
		"What one workload, named as in the cluster, opened connections to and what opened "+
			"connections to it, in a window. If several workloads share the name, the answer lists "+
			"them; call again with namespace or kind.", h.getDependencies)
	add(s, "diff_topology", "Compare two periods",
		"Compare the relationships of two equal-length periods: baseline [from, from+length) "+
			"against current [to-length, to). Lines are NEW, REMOVED or CHANGED baseline→current.",
		h.diffTopology)
	add(s, "get_edge", "Get one relationship",
		"One relationship by the id= a get_topology or diff_topology line gave, with when it was "+
			"first and last seen.", h.getEdge)
	return s
}

// Serve runs the server on t until the client disconnects or ctx ends.
func Serve(ctx context.Context, cfg Config, t mcp.Transport) error {
	return New(cfg).Run(ctx, t)
}

func add[In any](s *mcp.Server, name, title, description string, h func(context.Context, In) (string, error)) {
	closed := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        name,
		Title:       title,
		Description: description + "\n\n" + Caveats,
		Annotations: &mcp.ToolAnnotations{
			Title:          title,
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  &closed,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		text, err := h(ctx, in)
		if err != nil {
			// A plain error becomes a tool error (isError, the message as text), which the agent
			// sees and can relay; it is never a protocol error.
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})
}

// Tool inputs. Field descriptions become the inferred JSON schema's; omitempty marks optional.

type namespacesIn struct {
	Since string `json:"since,omitempty" jsonschema:"window ending now: 1m 5m 15m 1h 6h 24h or any Go duration such as 90m; default the server's"`
}

type topologyIn struct {
	Namespaces      []string `json:"namespaces,omitempty" jsonschema:"only relationships touching these namespaces"`
	Kind            string   `json:"kind,omitempty" jsonschema:"only nodes of this kind: Service Deployment StatefulSet DaemonSet Job Pod External"`
	Query           string   `json:"query,omitempty" jsonschema:"case-insensitive substring of a node name"`
	Since           string   `json:"since,omitempty" jsonschema:"window ending now, such as 1h; exclusive with from/to"`
	From            string   `json:"from,omitempty" jsonschema:"window start: now, a duration ago such as 2h, or an RFC 3339 time; needs to"`
	To              string   `json:"to,omitempty" jsonschema:"window end: now, a duration ago, or an RFC 3339 time; needs from"`
	IncludeExternal *bool    `json:"include_external,omitempty" jsonschema:"include the EXTERNAL node (default true)"`
}

type dependenciesIn struct {
	Name      string `json:"name" jsonschema:"workload name as in the cluster, such as backend"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace of the workload, when the name is in several"`
	Kind      string `json:"kind,omitempty" jsonschema:"kind of the workload, when several kinds share the name"`
	Since     string `json:"since,omitempty" jsonschema:"window ending now, such as 1h"`
}

type diffIn struct {
	From       string   `json:"from" jsonschema:"start of the baseline: a duration ago such as 2h, now, or an RFC 3339 time"`
	To         string   `json:"to" jsonschema:"end of the current period: now, a duration ago, or an RFC 3339 time"`
	Length     string   `json:"length,omitempty" jsonschema:"length of each period (default half of to minus from; longer would overlap and is refused)"`
	Namespaces []string `json:"namespaces,omitempty" jsonschema:"only relationships touching these namespaces"`
}

type edgeIn struct {
	EdgeID string `json:"edge_id" jsonschema:"the id= value from a get_topology or diff_topology line, verbatim"`
	Since  string `json:"since,omitempty" jsonschema:"window ending now, such as 1h"`
}

type handlers struct{ cfg Config }

func (h handlers) client(ctx context.Context) (*api.Client, error) {
	c, err := h.cfg.Client(ctx)
	if err != nil {
		return nil, h.failure(err)
	}
	return c, nil
}

func (h handlers) listNamespaces(ctx context.Context, in namespacesIn) (string, error) {
	w, err := query.Since(in.Since, h.cfg.Now())
	if err != nil {
		return "", err
	}
	c, err := h.client(ctx)
	if err != nil {
		return "", err
	}
	r, _, err := c.Namespaces(ctx, w)
	if err != nil {
		return "", h.failure(err)
	}
	return capped(render.Namespaces(r), false), nil
}

func (h handlers) getTopology(ctx context.Context, in topologyIn) (string, error) {
	q := api.GraphQuery{Namespaces: in.Namespaces, Query: in.Query}
	if in.IncludeExternal != nil && !*in.IncludeExternal {
		q.ExcludeExternal = true
	}
	var err error
	if in.Kind != "" {
		if q.Kind, err = query.CanonicalKind(in.Kind); err != nil {
			return "", err
		}
	}
	if q.Window, err = query.Window(in.Since, in.From, in.To, h.cfg.Now()); err != nil {
		return "", err
	}
	c, err := h.client(ctx)
	if err != nil {
		return "", err
	}
	r, _, err := c.Graph(ctx, q)
	if err != nil {
		return "", h.failure(err)
	}
	return capped(render.Graph(r, render.Options{IDs: true}), r.Summary.Truncated || r.Summary.TruncationReason != nil), nil
}

// getDependencies is `deps`: resolve the name against GET /graph?query=NAME by the nodes' name,
// namespace and kind fields, then read the one match by its ID, verbatim, over the window the
// graph answered for.
func (h handlers) getDependencies(ctx context.Context, in dependenciesIn) (string, error) {
	if in.Name == "" {
		return "", errors.New("name is required, such as backend")
	}
	kind := ""
	if in.Kind != "" {
		var err error
		if kind, err = query.CanonicalKind(in.Kind); err != nil {
			return "", err
		}
	}
	w, err := query.Since(in.Since, h.cfg.Now())
	if err != nil {
		return "", err
	}
	c, err := h.client(ctx)
	if err != nil {
		return "", err
	}
	graph, _, err := c.Graph(ctx, api.GraphQuery{Window: w, Query: in.Name})
	if err != nil {
		return "", h.failure(err)
	}
	node, err := query.Resolve(graph.Nodes, in.Name, in.Namespace, kind)
	var ambiguous *query.AmbiguousError
	switch {
	case errors.Is(err, query.ErrNotObserved):
		return notObserved(in, graph), nil
	case errors.As(err, &ambiguous):
		lines := []string{fmt.Sprintf("%q matches %d workloads; call again with namespace and kind:",
			in.Name, len(ambiguous.Candidates))}
		for _, n := range ambiguous.Candidates {
			ns := "(none)"
			if n.Namespace != nil {
				ns = *n.Namespace
			}
			lines = append(lines, fmt.Sprintf("  kind=%s namespace=%s", n.Kind, ns))
		}
		return "", errors.New(strings.Join(lines, "\n"))
	case err != nil:
		return "", err
	}
	detail, _, err := c.Node(ctx, node.ID, api.Window{From: graph.Window.Start, To: graph.Window.End})
	if err != nil {
		return "", h.failure(err)
	}
	return capped(render.Dependencies(detail, graph.Nodes, render.Options{IDs: true}), graph.Summary.Truncated), nil
}

func notObserved(in dependenciesIn, graph *api.GraphResponse) string {
	what := in.Name
	if in.Kind != "" {
		what = in.Kind + "/" + what
	}
	if in.Namespace != "" {
		what += " in namespace " + in.Namespace
	}
	msg := fmt.Sprintf("%s: not observed in this window (%s to %s); absent is not proof it does not exist, "+
		"so widen the window (since) before concluding that", what,
		graph.Window.Start.UTC().Format(time.RFC3339), graph.Window.End.UTC().Format(time.RFC3339))
	if graph.Summary.Truncated {
		msg += "\n" + serverTruncated
	}
	return msg + "\n"
}

func (h handlers) diffTopology(ctx context.Context, in diffIn) (string, error) {
	baseline, current, err := query.DiffPeriods(in.From, in.To, in.Length, h.cfg.Now())
	if err != nil {
		return "", err
	}
	c, err := h.client(ctx)
	if err != nil {
		return "", err
	}
	r, _, err := c.Diff(ctx, api.DiffQuery{Baseline: baseline, Current: current, Namespaces: in.Namespaces})
	if err != nil {
		return "", h.failure(err)
	}
	return capped(render.Diff(r, render.Options{IDs: true}), r.Summary.Truncated), nil
}

func (h handlers) getEdge(ctx context.Context, in edgeIn) (string, error) {
	if in.EdgeID == "" {
		return "", errors.New("edge_id is required: the id= value of a get_topology line")
	}
	w, err := query.Since(in.Since, h.cfg.Now())
	if err != nil {
		return "", err
	}
	c, err := h.client(ctx)
	if err != nil {
		return "", err
	}
	r, _, err := c.Edge(ctx, in.EdgeID, w)
	if err != nil {
		return "", h.failure(err)
	}
	return capped(render.Edge(r, render.Options{IDs: true}), false), nil
}

// failure turns a client error into the tool error the agent relays. Anything that only a sign-in
// can fix reads exactly as D-15.5 words it, naming the login command for the human to run.
func (h handlers) failure(err error) error {
	var (
		notSignedIn *auth.NotSignedInError
		issuerDown  *auth.ConnectionError
		tokenErr    *api.TokenError
		connErr     *api.ConnectionError
	)
	login := fmt.Sprintf("not signed in to %s: run \"kubectl topology login --server %s\"", h.cfg.Server, h.cfg.Server)
	switch {
	case errors.As(err, &notSignedIn):
		return errors.New(login)
	case errors.As(err, &issuerDown):
		// The refresh could not be sent; the tokens are kept, so the human need not sign in.
		return fmt.Errorf("refreshing the sign-in: %v (is the port-forward to %s running?)", issuerDown, h.cfg.Server)
	case errors.As(err, &tokenErr):
		return fmt.Errorf("cannot use the stored sign-in for %s: %v", h.cfg.Server, tokenErr.Err)
	case api.IsUnauthorized(err):
		return fmt.Errorf("%s\n(the server refused the request: signed out, expired, or not admitted)", login)
	case errors.As(err, &connErr):
		return fmt.Errorf("%v (is the port-forward to %s running?)", connErr, h.cfg.Server)
	}
	return err
}

const serverTruncated = "the server truncated this result: relationships are missing; narrow the query " +
	"(namespaces, kind, query, or a shorter window) before concluding anything is absent"

// capped joins t, keeping at most MaxRows relationship lines and MaxBytes in all, and ends with a
// line saying how many were left out and how to narrow the query. serverTrunc adds the same
// warning for the backend's own truncation, which the header already reports as a fact.
func capped(t render.Text, serverTrunc bool) string {
	var tail []string
	if serverTrunc {
		tail = append(tail, serverTruncated)
	}
	size := func(lines []string) int {
		n := 0
		for _, l := range lines {
			n += len(l) + 1
		}
		return n
	}
	fixed := size(t.Header) + size(t.Notes) + size(tail)
	omitLine := func(n int) string {
		return fmt.Sprintf("%d more relationships omitted (output capped at %d lines / %d KiB); narrow the "+
			"query with namespaces, kind, query or a shorter window", n, MaxRows, MaxBytes>>10)
	}
	rows := t.Rows
	kept := len(rows)
	if kept > MaxRows {
		kept = MaxRows
	}
	for kept > 0 {
		used := fixed + size(rows[:kept])
		if kept < len(rows) {
			used += len(omitLine(len(rows)-kept)) + 1
		}
		if used <= MaxBytes {
			break
		}
		kept--
	}
	out := render.Text{Header: t.Header, Rows: rows[:kept], Notes: append([]string(nil), t.Notes...)}
	if kept < len(rows) {
		out.Notes = append(out.Notes, omitLine(len(rows)-kept))
	}
	out.Notes = append(out.Notes, tail...)
	return out.String()
}
