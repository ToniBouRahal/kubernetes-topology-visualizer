import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchDiff, fetchGraph } from "../../api/client";
import type { DiffQuery, DiffResponse, GraphNode, GraphQuery } from "../../api/types";

export interface DiffState {
  diff: DiffResponse | null;
  /**
   * Every node appearing in either period, keyed by id.
   *
   * A comparison routinely references nodes the live graph has never shown — anything that existed
   * in the baseline period and has since disappeared is exactly what REMOVED means. Those nodes
   * still need a kind, a namespace and a name to render.
   *
   * The ids carry that information in their structure, and an earlier version of the compare
   * canvas took it by splitting on ":". That is precisely what `contracts/ids.md` §2 forbids:
   * ids are opaque, and a consumer that parses them silently produces wrong labels the moment the
   * format gains a segment. So both periods' graphs are fetched instead and the real node records
   * are used.
   */
  nodes: Map<string, GraphNode>;
  loading: boolean;
  error: string | null;
  refresh: () => void;
}

/** The graph query covering one period of a comparison, carrying the same filters. */
function periodQuery(query: DiffQuery, from: string, to: string): GraphQuery {
  return {
    from,
    to,
    namespace: query.namespace,
    kind: query.kind,
    query: query.query,
    includeExternal: query.includeExternal,
  };
}

/**
 * Fetches a comparison.
 *
 * Unlike the live graph this does NOT poll: both periods are fixed, so re-fetching would return
 * the same answer while adding load. A comparison is a question asked once (ADR-006 D-6.5).
 */
export function useDiff(query: DiffQuery | null): DiffState {
  const [diff, setDiff] = useState<DiffResponse | null>(null);
  const [nodes, setNodes] = useState<Map<string, GraphNode>>(new Map());
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inFlight = useRef<AbortController | null>(null);

  const signature = query ? JSON.stringify(query) : null;

  const load = useCallback(async () => {
    if (!signature) {
      setDiff(null);
      setNodes(new Map());
      return;
    }

    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;
    setLoading(true);

    try {
      const parsed = JSON.parse(signature) as DiffQuery;

      // All three in parallel: the two period graphs are what make the diff renderable, so
      // serialising them would triple the time the user waits for a comparison.
      const [diffResult, baselineGraph, currentGraph] = await Promise.all([
        fetchDiff(parsed, controller.signal),
        fetchGraph(
          periodQuery(parsed, parsed.baselineFrom, parsed.baselineTo),
          controller.signal,
        ),
        fetchGraph(periodQuery(parsed, parsed.currentFrom, parsed.currentTo), controller.signal),
      ]);

      // Current period last, so a node present in both periods is described by its most recent
      // record rather than a stale one.
      const merged = new Map<string, GraphNode>();
      for (const node of [...baselineGraph.nodes, ...currentGraph.nodes]) {
        merged.set(node.id, node);
      }

      setDiff(diffResult);
      setNodes(merged);
      setError(null);
    } catch (err) {
      if (controller.signal.aborted) return;
      setError(
        err instanceof ApiError
          ? err.detail
          : err instanceof Error
            ? err.message
            : "the comparison could not be loaded",
      );
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [signature]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => () => inFlight.current?.abort(), []);

  return { diff, nodes, loading, error, refresh: () => void load() };
}
