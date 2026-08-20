import dagre from "@dagrejs/dagre";

import type { GraphEdge, GraphNode } from "../../api/types";

export const NODE_WIDTH = 210;
export const NODE_HEIGHT = 56;

export interface Position {
  x: number;
  y: number;
}

/** Positions keyed by node id, carried across polls. */
export type PositionCache = Map<string, Position>;

function topologySignature(nodes: GraphNode[], edges: GraphEdge[]): string {
  // Identity only. Counts and timestamps change on every poll and must NOT trigger a re-layout;
  // only a change in the SET of nodes or edges can move anything.
  return [
    nodes.map((n) => n.id).sort().join("|"),
    edges.map((e) => e.id).sort().join("|"),
  ].join("::");
}

export interface LayoutResult {
  positions: PositionCache;
  signature: string;
  /** False when the cache was reused, which is the common case between polls. */
  recomputed: boolean;
}

/**
 * Lay the graph out, reusing cached positions whenever the topology is unchanged.
 *
 * Dagre is deterministic for identical input, but node ORDER changes between polls would still
 * move everything. Two defences (ADR-006 D-6.2):
 *
 *   1. Nodes and edges are sorted before Dagre sees them, so equal input produces equal output.
 *   2. When the identity signature is unchanged, layout is skipped entirely and cached positions
 *      are returned — the graph literally cannot move while only counts are updating.
 *
 * When the topology does change, surviving nodes keep their cached positions and only genuinely
 * new nodes are placed. One new edge must not rearrange the whole graph mid-demonstration.
 */
export function layoutGraph(
  nodes: GraphNode[],
  edges: GraphEdge[],
  previous?: { positions: PositionCache; signature: string },
): LayoutResult {
  const signature = topologySignature(nodes, edges);

  if (previous && previous.signature === signature) {
    return { positions: previous.positions, signature, recomputed: false };
  }

  const graph = new dagre.graphlib.Graph();
  graph.setDefaultEdgeLabel(() => ({}));
  graph.setGraph({ rankdir: "LR", ranksep: 130, nodesep: 70, marginx: 24, marginy: 24 });

  // Deterministic insertion order: Dagre's output depends on it.
  for (const node of [...nodes].sort((a, b) => a.id.localeCompare(b.id))) {
    graph.setNode(node.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
  }
  for (const edge of [...edges].sort((a, b) => a.id.localeCompare(b.id))) {
    // Dagre would otherwise create a phantom node for an endpoint it has not seen.
    if (graph.hasNode(edge.source_id) && graph.hasNode(edge.target_id)) {
      graph.setEdge(edge.source_id, edge.target_id);
    }
  }

  dagre.layout(graph);

  const positions: PositionCache = new Map();
  for (const node of nodes) {
    const cached = previous?.positions.get(node.id);
    if (cached) {
      // A node that already existed keeps exactly where it was.
      positions.set(node.id, cached);
      continue;
    }
    const laid = graph.node(node.id);
    positions.set(
      node.id,
      laid
        ? // Dagre reports centres; React Flow positions by top-left corner.
          { x: laid.x - NODE_WIDTH / 2, y: laid.y - NODE_HEIGHT / 2 }
        : { x: 0, y: 0 },
    );
  }

  return { positions, signature, recomputed: true };
}

/**
 * Edge thickness from a capped logarithmic scale (ADR-006 D-6.4).
 *
 * Logarithmic because connection counts span orders of magnitude and a linear scale would render
 * everything below the busiest edge as a hairline. Capped so one extreme edge cannot dominate.
 */
export function edgeWidth(value: number, max: number): number {
  const MIN = 1.5;
  const MAX = 6;
  if (value <= 0 || max <= 0) return MIN;
  const scaled = Math.log1p(value) / Math.log1p(max);
  return Math.min(MAX, MIN + scaled * (MAX - MIN));
}
