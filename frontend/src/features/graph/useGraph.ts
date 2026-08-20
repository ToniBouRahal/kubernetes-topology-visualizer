import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchGraph } from "../../api/client";
import type { GraphQuery, GraphResponse } from "../../api/types";

/** Poll interval fixed by ADR-001 §5.6. SSE is deferred (§13). */
export const POLL_INTERVAL_MS = 5_000;

export interface GraphState {
  /** The last SUCCESSFUL response. Never cleared by a failure. */
  graph: GraphResponse | null;
  /** True only while the very first load is in flight; a refresh does not blank the canvas. */
  initialLoading: boolean;
  refreshing: boolean;
  /** Set on failure while `graph` keeps the last good data (ADR-006 D-6.6). */
  error: string | null;
  lastUpdated: Date | null;
  refresh: () => void;
}

/**
 * Polls the graph API.
 *
 * Three behaviours here are requirements rather than conveniences:
 *
 *  - A failed request NEVER clears the last successful graph. A transient backend blip must not
 *    blank a demo (F7, test T-6.6).
 *  - Every request is cancellable, and a response is applied only if it belongs to the most
 *    recent request. Without the sequence check, a slow response overtaking a fast one would
 *    silently roll the view backwards (F8, test T-6.10).
 *  - Polling stops while paused, and in history mode, where the window is fixed and re-fetching
 *    only adds load.
 */
export function useGraph(query: GraphQuery, options: { paused?: boolean } = {}): GraphState {
  const { paused = false } = options;

  const [graph, setGraph] = useState<GraphResponse | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  const inFlight = useRef<AbortController | null>(null);
  const latestSequence = useRef(0);
  const hasLoaded = useRef(false);

  // Serialised so the effect below re-runs on a real filter change rather than on every render.
  const querySignature = JSON.stringify(query);

  const load = useCallback(async () => {
    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;

    const sequence = ++latestSequence.current;
    setRefreshing(true);

    try {
      const response = await fetchGraph(JSON.parse(querySignature) as GraphQuery, controller.signal);

      // A response from a superseded request must not overwrite newer data.
      if (sequence !== latestSequence.current) return;

      setGraph(response);
      setError(null);
      setLastUpdated(new Date());
      hasLoaded.current = true;
    } catch (err) {
      if (controller.signal.aborted || sequence !== latestSequence.current) return;

      setError(
        err instanceof ApiError
          ? `${err.detail}${err.requestId ? ` (request ${err.requestId})` : ""}`
          : err instanceof Error
            ? err.message
            : "the graph could not be loaded",
      );
      // `graph` is deliberately untouched: the last good topology stays on screen.
    } finally {
      if (sequence === latestSequence.current) {
        setRefreshing(false);
        setInitialLoading(false);
      }
    }
  }, [querySignature]);

  useEffect(() => {
    void load();
    if (paused) return;

    const timer = setInterval(() => void load(), POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [load, paused]);

  // Cancel anything in flight when the component goes away.
  useEffect(() => () => inFlight.current?.abort(), []);

  return {
    graph,
    initialLoading: initialLoading && !hasLoaded.current,
    refreshing,
    error,
    lastUpdated,
    refresh: () => void load(),
  };
}
