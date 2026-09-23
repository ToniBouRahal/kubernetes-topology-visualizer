import dagre from "@dagrejs/dagre";

import type { GraphEdge, GraphNode } from "../../api/types";

/**
 * Node geometry (ADR-010 D-10.2, D-10.3).
 *
 * A node is a circle with its own name set inside it, and a namespace label in a band beneath.
 * The diameter grows with DEGREE — how many distinct components this one was observed talking to —
 * which is the graph's one otherwise-unused ordinal channel. It is bounded at both ends: too small
 * and the name will not fit, too large and one hub swamps the canvas.
 */
export const NODE_MIN_DIAMETER = 84;
export const NODE_MAX_DIAMETER = 132;
/** The namespace label sits below the circle and is part of the node's box, so dagre must count it. */
export const NODE_LABEL_BAND = 18;

export function nodeDiameter(degree: number): number {
  return Math.min(NODE_MAX_DIAMETER, NODE_MIN_DIAMETER + Math.max(0, degree) * 8);
}

/**
 * How many DISTINCT components each node talks to, in either direction.
 *
 * Distinct, not edge count: two components that talk on three ports are one dependency observed
 * three ways, and sizing by edge count would make a chatty pair look like a hub. A self-loop — the
 * namespace-grouped view draws them — is not another component and does not count.
 */
export function degreesOf(nodes: GraphNode[], edges: GraphEdge[]): Map<string, number> {
  const neighbours = new Map<string, Set<string>>();
  for (const node of nodes) neighbours.set(node.id, new Set());
  for (const edge of edges) {
    if (edge.source_id === edge.target_id) continue;
    neighbours.get(edge.source_id)?.add(edge.target_id);
    neighbours.get(edge.target_id)?.add(edge.source_id);
  }
  const degrees = new Map<string, number>();
  for (const [id, set] of neighbours) degrees.set(id, set.size);
  return degrees;
}

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
  /** Degrees size the nodes (D-10.3). Omitted, every node gets the minimum diameter. */
  degrees?: Map<string, number>,
): LayoutResult {
  const signature = topologySignature(nodes, edges);

  if (previous && previous.signature === signature) {
    return { positions: previous.positions, signature, recomputed: false };
  }

  // A node's box is its circle plus the namespace band beneath it. Degree only ever changes when
  // the edge SET changes, which already moves the signature above, so a resize can never be
  // hiding behind a cache hit.
  const boxOf = (id: string) => {
    const diameter = nodeDiameter(degrees?.get(id) ?? 0);
    return { width: diameter, height: diameter + NODE_LABEL_BAND };
  };

  const graph = new dagre.graphlib.Graph();
  graph.setDefaultEdgeLabel(() => ({}));
  // ranksep leaves room for the port/count label to sit on the edge without colliding with either
  // endpoint. nodesep was tight when a node was a 224px-wide card and the graph was wide rather
  // than tall; a circle is narrower and taller than that card, so the crowding moved to the
  // vertical axis and the separation follows it (ADR-010 D-10.2).
  graph.setGraph({ rankdir: "LR", ranksep: 150, nodesep: 64, marginx: 24, marginy: 24 });

  // Deterministic insertion order: Dagre's output depends on it.
  for (const node of [...nodes].sort((a, b) => a.id.localeCompare(b.id))) {
    graph.setNode(node.id, boxOf(node.id));
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
    const box = boxOf(node.id);
    positions.set(
      node.id,
      laid
        ? // Dagre reports centres; React Flow positions by top-left corner.
          { x: laid.x - box.width / 2, y: laid.y - box.height / 2 }
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
