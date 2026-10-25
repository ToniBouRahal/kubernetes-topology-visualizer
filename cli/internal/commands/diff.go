package commands

import (
	"context"
	"errors"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/query"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/render"
)

// runDiff compares two equal-length periods: baseline [A, A+L) and current [B−L, B), L half of
// B − A unless --length says otherwise. "diff --from 2h --to now" is the hour before last
// against the last hour.
func runDiff(ctx context.Context, env Env, args []string) int {
	var (
		c                common
		namespaces       multi
		from, to, length string
	)
	fs := newFlagSet(env, "diff", "diff --from T --to T [--length D] [-n NS]...")
	c.register(fs)
	fs.StringVar(&from, "from", "", "start of the baseline: now, a duration ago (2h), or RFC 3339")
	fs.StringVar(&to, "to", "", "end of the current period: now, a duration ago, or RFC 3339")
	fs.StringVar(&length, "length", "", "length of each period (default: half of --to minus --from)")
	fs.Var(&namespaces, "n", "namespace (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return usageError(env, err)
	}
	if len(pos) > 0 {
		return usageError(env, errors.New("diff takes no arguments"))
	}
	if err := c.validate(); err != nil {
		return usageError(env, err)
	}
	baseline, current, err := query.DiffPeriods(from, to, length, env.Now())
	if err != nil {
		return usageError(env, err)
	}
	client, code := connect(ctx, env, c.client)
	if client == nil {
		return code
	}
	resp, raw, err := client.Diff(ctx, api.DiffQuery{Baseline: baseline, Current: current, Namespaces: namespaces})
	if err != nil {
		return apiFailure(env, client.Server(), err)
	}
	return emit(env, c.output, raw, render.Diff(resp, render.Options{}))
}
