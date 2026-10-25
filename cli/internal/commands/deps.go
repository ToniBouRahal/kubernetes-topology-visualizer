package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/query"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/render"
)

// runDeps answers "what does NAME talk to, and what talks to it?". The name is resolved against
// the nodes of GET /graph?query=NAME by their name, namespace and kind fields; the one match's ID
// goes to GET /nodes/{id} verbatim. The node is read over the exact window the graph answered
// for, so a preset cannot slide between the two requests.
func runDeps(ctx context.Context, env Env, args []string) int {
	var (
		c                      common
		namespace, kind, since string
	)
	fs := newFlagSet(env, "deps", "deps NAME [-n NS] [--kind K] [--since D]")
	c.register(fs)
	fs.StringVar(&namespace, "n", "", "namespace of NAME, when the name is in several")
	fs.StringVar(&kind, "kind", "", "kind of NAME, when several kinds share it")
	fs.StringVar(&since, "since", "", "window: a preset (1m 5m 15m 1h 6h 24h) or any duration ago")
	pos, err := parse(fs, args)
	if err != nil {
		return usageError(env, err)
	}
	if len(pos) != 1 || pos[0] == "" {
		return usageError(env, errors.New("deps takes exactly one NAME, such as backend"))
	}
	name := pos[0]
	if err := c.validate(); err != nil {
		return usageError(env, err)
	}
	if kind != "" {
		if kind, err = query.CanonicalKind(kind); err != nil {
			return usageError(env, err)
		}
	}
	w, err := query.Since(since, env.Now())
	if err != nil {
		return usageError(env, err)
	}
	client, code := connect(ctx, env, c.client)
	if client == nil {
		return code
	}

	graph, _, err := client.Graph(ctx, api.GraphQuery{Window: w, Query: name})
	if err != nil {
		return apiFailure(env, client.Server(), err)
	}
	node, err := query.Resolve(graph.Nodes, name, namespace, kind)
	var ambiguous *query.AmbiguousError
	switch {
	case errors.Is(err, query.ErrNotObserved):
		return notObserved(env, c.output, name, namespace, kind, graph)
	case errors.As(err, &ambiguous):
		fmt.Fprintf(env.Stderr, "error: %v:\n", ambiguous)
		for _, n := range ambiguous.Candidates {
			ns := "(no namespace)"
			if n.Namespace != nil {
				ns = "-n " + *n.Namespace
			}
			fmt.Fprintf(env.Stderr, "  --kind %s %s\n", n.Kind, ns)
		}
		return ExitUsage
	}

	window := api.Window{From: graph.Window.Start, To: graph.Window.End}
	detail, raw, err := client.Node(ctx, node.ID, window)
	if err != nil {
		return apiFailure(env, client.Server(), err)
	}
	return emit(env, c.output, raw, render.Dependencies(detail, graph.Nodes, render.Options{}))
}

// notObserved is a successful, empty answer: the name had no traffic in the window. Absent is
// not proof it does not exist (limitations §1.4), so the message never says so.
func notObserved(env Env, output, name, namespace, kind string, graph *api.GraphResponse) int {
	what := name
	if kind != "" {
		what = kind + "/" + name
	}
	if namespace != "" {
		what += " in namespace " + namespace
	}
	msg := fmt.Sprintf("%s: not observed in this window (%s to %s)", what,
		graph.Window.Start.UTC().Format(time.RFC3339),
		graph.Window.End.UTC().Format(time.RFC3339))
	if graph.Summary.Truncated {
		msg += "; the server truncated the graph, so narrow the window before concluding that"
	}
	if output == "json" {
		// stdout stays empty: there is no node payload to print unchanged.
		fmt.Fprintln(env.Stderr, msg)
		return ExitOK
	}
	fmt.Fprintln(env.Stdout, msg)
	return ExitOK
}
