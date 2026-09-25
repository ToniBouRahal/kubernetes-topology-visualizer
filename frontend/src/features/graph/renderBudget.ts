import type { GraphEdge, GraphNode } from "../../api/types";

/**
 * The most edges this canvas draws: the largest graph measured to stay responsive.
 *
 * Measured with bench/ (docs/limitations.md §4.1), production build, 1280x720:
 *
 *   500 nodes / 2,000 edges   first paint 1.1 s, selecting a component 81 ms, idle polls 0 ms
 *
 * Until P5-F18 this was 400, because 1,000 edges never painted. The cause turned out to be
 * Dagre's layout on dense graphs, not the DOM (see FAST_LAYOUT_EDGES in layout.ts).
 *
 * 2,000 matches the backend's default GRAPH_MAX_EDGES, so normally this never triggers. It stays
 * because an operator can raise that setting past anything measured here, and a graph that says
 * what it left out beats one that locks the tab.
 *
 * VITE_MAX_RENDERED_EDGES exists only so the benchmark can measure past the cap.
 */
export const MAX_RENDERED_EDGES = Number(import.meta.env.VITE_MAX_RENDERED_EDGES) || 2000;

export interface RenderBudget {
  nodes: GraphNode[];
  edges: GraphEdge[];
  /** Null when everything fits. Otherwise what was dropped, for the banner to explain. */
  capped: { shownEdges: number; totalEdges: number; hiddenNodes: number } | null;
}

/**
 * Keep the busiest edges when a graph exceeds what can be drawn.
 *
 * Busiest by successful connection count (failure-only edges may be hidden), because an arbitrary subset would be worse than useless — it would
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
