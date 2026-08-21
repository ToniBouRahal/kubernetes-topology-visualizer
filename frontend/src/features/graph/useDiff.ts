import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchDiff } from "../../api/client";
import type { DiffQuery, DiffResponse } from "../../api/types";

export interface DiffState {
  diff: DiffResponse | null;
  loading: boolean;
  error: string | null;
  refresh: () => void;
}

/**
 * Fetches a comparison.
 *
 * Unlike the live graph this does NOT poll: both periods are fixed, so re-fetching would return
 * the same answer while adding load. A comparison is a question asked once (ADR-006 D-6.5).
 */
export function useDiff(query: DiffQuery | null): DiffState {
  const [diff, setDiff] = useState<DiffResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inFlight = useRef<AbortController | null>(null);

  const signature = query ? JSON.stringify(query) : null;

  const load = useCallback(async () => {
    if (!signature) {
      setDiff(null);
      return;
    }

    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;
    setLoading(true);

    try {
      setDiff(await fetchDiff(JSON.parse(signature) as DiffQuery, controller.signal));
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

  return { diff, loading, error, refresh: () => void load() };
}
