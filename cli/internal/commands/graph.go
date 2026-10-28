package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/query"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/render"
)

func runGraph(ctx context.Context, env Env, args []string) int {
	var (
		c                    common
		namespaces           multi
		kind, q              string
		since, from, to      string
		noExternal, unresolv bool
	)
	fs := newFlagSet(env, "graph",
		"graph [-n NS]... [--kind K] [--query Q] [--since D | --from T --to T] [--no-external] [--unresolved] [-o text|json|mermaid]")
	c.register(fs)
	fs.Lookup("o").Usage = "output format: text, json (the API response unchanged), or mermaid (a diagram)"
	fs.Var(&namespaces, "n", "namespace (repeatable)")
	fs.StringVar(&kind, "kind", "", "only nodes of this kind")
	fs.StringVar(&q, "query", "", "case-insensitive substring of a node name")
	fs.StringVar(&since, "since", "", "window: a preset (1m 5m 15m 1h 6h 24h) or any duration ago")
	fs.StringVar(&from, "from", "", "start: now, a duration ago (2h), or RFC 3339")
	fs.StringVar(&to, "to", "", "end: now, a duration ago, or RFC 3339")
	fs.BoolVar(&noExternal, "no-external", false, "leave out the EXTERNAL node")
	fs.BoolVar(&unresolv, "unresolved", false, "include endpoints the agent could not attribute")
	pos, err := parse(fs, args)
	if err != nil {
		return usageError(env, err)
	}
	if len(pos) > 0 {
		return usageError(env, errors.New("graph takes no arguments; use --query to filter by name"))
	}
	if err := c.validate("mermaid"); err != nil {
		return usageError(env, err)
	}
	gq := api.GraphQuery{Namespaces: namespaces, Query: q, ExcludeExternal: noExternal, IncludeUnresolved: unresolv}
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
	resp, raw, err := client.Graph(ctx, gq)
	if err != nil {
		return apiFailure(env, client.Server(), err)
	}
	if c.output == "mermaid" {
		// Built from the response, so it carries the window and filters asked for (D-16.7).
		_, _ = fmt.Fprint(env.Stdout, render.Mermaid(resp))
		return ExitOK
	}
	return emit(env, c.output, raw, render.Graph(resp, render.Options{}))
}
