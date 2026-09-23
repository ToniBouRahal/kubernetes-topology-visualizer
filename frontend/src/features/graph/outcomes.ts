import type { GraphEdge, NodeDependency } from "../../api/types";

/**
 * The same reading, broken into fields for the details panel (ADR-010 D-10.1).
 *
 * One source of truth with `outcomeLabel` below, which composes the same facts into the single
 * line an edge label has room for. The distinction the panel must not lose: a failure count that
 * was never measured is not a zero, and a mean with no samples behind it is not 0 ms.
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

/** Counts cover observed outcomes; missing failure measurement is never a zero. */
export function outcomeLabel(outcome: GraphEdge | NodeDependency): string {
  const failures = outcome.failed_connection_count == null
    ? "failed/aborted unmeasured" : `${outcome.failed_connection_count} failed/aborted`;
  const samples = outcome.connect_latency_count ?? 0;
  const timing = samples > 0
    ? ` · mean TCP setup ${Number(((outcome.connect_latency_sum_us ?? 0) / samples / 1000).toFixed(3))} ms`
    : "";
  return `${outcome.connection_count} successful · ${failures}${timing}`;
}
