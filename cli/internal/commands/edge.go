package commands

import (
	"context"
	"errors"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/query"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/render"
)

// runEdge takes an edge ID the user copied from -o json or an MCP tool's output, and passes it
// on verbatim.
func runEdge(ctx context.Context, env Env, args []string) int {
	var (
		c     common
		since string
	)
	fs := newFlagSet(env, "edge", "edge EDGE_ID [--since D]")
	c.register(fs)
	fs.StringVar(&since, "since", "", "window: a preset (1m 5m 15m 1h 6h 24h) or any duration ago")
	pos, err := parse(fs, args)
	if err != nil {
		return usageError(env, err)
	}
	if len(pos) != 1 || pos[0] == "" {
		return usageError(env, errors.New("edge takes exactly one EDGE_ID (from -o json output)"))
	}
	if err := c.validate(); err != nil {
		return usageError(env, err)
	}
	w, err := query.Since(since, env.Now())
	if err != nil {
		return usageError(env, err)
	}
	client, code := connect(ctx, env, c.client)
	if client == nil {
		return code
	}
	resp, raw, err := client.Edge(ctx, pos[0], w)
	if err != nil {
		return apiFailure(env, client.Server(), err)
	}
	return emit(env, c.output, raw, render.Edge(resp, render.Options{}))
}
