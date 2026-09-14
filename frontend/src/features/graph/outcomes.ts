import type { GraphEdge, NodeDependency } from "../../api/types";

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
