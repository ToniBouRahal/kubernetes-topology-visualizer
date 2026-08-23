import type { GraphEdge, GraphNode } from "../../api/types";

/**
 * The number of edges this canvas can draw before the browser stops responding.
 *
 * Measured, not guessed (docs/limitations.md §4.1):
 *
 *   102 nodes /   307 edges   paints in 1.06 s, frames answered in 2 ms
 *   172 nodes / 1,002 edges   never painted within 250 s
 *   500 nodes / 1,908 edges   page stopped responding entirely
 *
 * The interface does not degrade past roughly 300 edges — it stops. React Flow builds a DOM
 * element per node and per edge, plus a text label per edge, and somewhere past 300 edges that
 * work exceeds what the main thread can finish.
 *
 * 400 sits above the largest graph observed to render comfortably and far below the smallest
 * observed to hang. It is deliberately a round number rather than a fitted one: the true cliff
 * depends on the machine, and pretending to know it to three significant figures would be worse
 * than picking a defensibly safe value.
 *
 * The real fix is edge virtualisation or canvas rendering. This is the honest interim: a graph
 * that says what it left out beats one that locks the tab.
 */
export const MAX_RENDERED_EDGES = 400;

export interface RenderBudget {
  nodes: GraphNode[];
  edges: GraphEdge[];
  /** Null when everything fits. Otherwise what was dropped, for the banner to explain. */
  capped: { shownEdges: number; totalEdges: number; hiddenNodes: number } | null;
}

/**
 * Keep the busiest edges when a graph exceeds what can be drawn.
 *
 * Busiest by connection count, because an arbitrary subset would be worse than useless — it would
 * look like a complete graph while hiding whichever relationships happened to sort last. The
 * heaviest edges are also the ones a reader is most likely to be looking for.
 *
 * Nodes left with no remaining edge are dropped too: a node is only in the graph because an edge
 * put it there (ADR-004 D-4.4), so an isolated node here would be an artefact of the cap rather
 * than something the cluster did.
 */
export function applyRenderBudget(
  nodes: GraphNode[],
  edges: GraphEdge[],
  limit: number = MAX_RENDERED_EDGES,
): RenderBudget {
  if (edges.length <= limit) {
    return { nodes, edges, capped: null };
  }

  // Sorted copy: mutating the caller's array would reorder the data behind the details panel.
  const kept = [...edges]
    .sort((a, b) => b.connection_count - a.connection_count || a.id.localeCompare(b.id))
    .slice(0, limit);

  const referenced = new Set<string>();
  for (const e of kept) {
    referenced.add(e.source_id);
    referenced.add(e.target_id);
  }
  const keptNodes = nodes.filter((n) => referenced.has(n.id));

  return {
    nodes: keptNodes,
    edges: kept,
    capped: {
      shownEdges: kept.length,
      totalEdges: edges.length,
      hiddenNodes: nodes.length - keptNodes.length,
    },
  };
}
