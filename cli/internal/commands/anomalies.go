package commands

import (
	"context"
	"errors"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/query"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/render"
)

// runAnomalies prints the backend's rule-based anomalies for a window (ADR-016 D-16.3). The
// baseline is the server's — the configured hours immediately before the window — so the CLI
// takes only the window and the graph's filters.
func runAnomalies(ctx context.Context, env Env, args []string) int {
	var (
		c               common
		namespaces      multi
		kind, q         string
		since, from, to string
		noExternal      bool
	)
	fs := newFlagSet(env, "anomalies",
		"anomalies [-n NS]... [--kind K] [--query Q] [--since D | --from T --to T] [--no-external]")
	c.register(fs)
	fs.Var(&namespaces, "n", "namespace (repeatable)")
	fs.StringVar(&kind, "kind", "", "only edges touching nodes of this kind")
	fs.StringVar(&q, "query", "", "case-insensitive substring of a node name")
	fs.StringVar(&since, "since", "", "window: a preset (1m 5m 15m 1h 6h 24h) or any duration ago")
	fs.StringVar(&from, "from", "", "start: now, a duration ago (2h), or RFC 3339")
	fs.StringVar(&to, "to", "", "end: now, a duration ago, or RFC 3339")
	fs.BoolVar(&noExternal, "no-external", false, "leave out the EXTERNAL node")
	pos, err := parse(fs, args)
	if err != nil {
		return usageError(env, err)
	}
	if len(pos) > 0 {
		return usageError(env, errors.New("anomalies takes no arguments; use --query to filter by name"))
	}
	if err := c.validate(); err != nil {
		return usageError(env, err)
	}
	gq := api.GraphQuery{Namespaces: namespaces, Query: q, ExcludeExternal: noExternal}
	if kind != "" {
		if gq.Kind, err = query.CanonicalKind(kind); err != nil {
			return usageError(env, err)
		}
	}
	if gq.Window, err = query.Window(since, from, to, env.Now()); err != nil {
		return usageError(env, err)
	}
	client, code := connect(ctx, env, c.client)
	if client == nil {
		return code
	}
	resp, raw, err := client.Anomalies(ctx, gq)
	if err != nil {
		return apiFailure(env, client.Server(), err)
	}
	return emit(env, c.output, raw, render.Anomalies(resp, render.Options{}))
}
