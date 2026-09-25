import type { GraphEdge, NodeDependency } from "../../api/types";

/**
 * A link's outcome, broken into fields for the details panel (ADR-010 D-10.1). Edges on the canvas
 * carry no text; this is where the reading is shown.
 *
 * The distinction the panel must not lose: a failure count that was never measured is not a zero,
 * and a mean with no samples behind it is not 0 ms.
 */
export function outcomeParts(outcome: GraphEdge | NodeDependency): {
  successful: number;
  failed: number | null;
  timing: string;
} {
  const samples = outcome.connect_latency_count ?? 0;
  return {
    successful: outcome.connection_count,
    failed: outcome.failed_connection_count ?? null,
    timing:
      samples > 0
        ? `mean TCP setup ${Number(((outcome.connect_latency_sum_us ?? 0) / samples / 1000).toFixed(3))} ms`
        : "TCP setup time unmeasured",
  };
}
