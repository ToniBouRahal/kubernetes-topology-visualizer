import { useEffect, useRef, useState } from "react";

import { fetchAnomalies } from "../../api/client";
import type { AnomalyResponse, GraphQuery } from "../../api/types";

/**
 * Anomalies for the period the graph shows (ADR-016 D-16.3).
 *
 * Follows the graph rather than polling on its own: it refetches when the query changes and each
 * time the graph reports a fresh reading, so the list and the canvas describe the same moment.
 * `null` switches it off — compare mode has its own classification and no single window.
 *
 * A failure keeps the last good answer FOR THE SAME QUERY and is otherwise silent. The list is an
 * aid to reading the graph; it must never be the reason the graph looks broken. An answer for a
 * different window or filter is never shown: findings that no longer describe the period on screen
 * are worse than none.
 */
export function useAnomalies(
  query: GraphQuery | null,
  lastUpdated: Date | null,
): AnomalyResponse | null {
  const [held, setHeld] = useState<{ signature: string; response: AnomalyResponse } | null>(null);
  const latestSequence = useRef(0);

  const signature = query ? JSON.stringify(query) : null;
  const tick = lastUpdated?.getTime() ?? 0;

  useEffect(() => {
    if (signature === null) return;
    const controller = new AbortController();
    const sequence = ++latestSequence.current;
    fetchAnomalies(JSON.parse(signature) as GraphQuery, controller.signal)
      .then((response) => {
        // A slow answer to a superseded query must not replace a newer one. A body without the
        // list (an older backend without the route, a proxy page) is treated as a failure rather
        // than rendered: the panel would otherwise throw and take the whole page with it.
        if (sequence === latestSequence.current && Array.isArray(response?.anomalies)) {
          setHeld({ signature, response });
        }
      })
      .catch(() => {
        /* Keep the last good list; see the comment above. */
      });
    return () => controller.abort();
  }, [signature, tick]);

  return held !== null && held.signature === signature ? held.response : null;
}
