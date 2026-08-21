import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  type Edge,
  type Node,
} from "@xyflow/react";
import { useMemo, useRef } from "react";

import type { DiffResponse, GraphNode } from "../../api/types";
import { TopologyNode, type TopologyNodeData } from "./TopologyNode";
import { diffStyle } from "./diffEncoding";
import { layoutGraph, type PositionCache } from "./layout";

const NODE_TYPES = { topology: TopologyNode };

/**
 * The comparison view.
 *
 * A diff response carries edges but no nodes — nodes are implied by the edges, and a REMOVED edge
 * still needs its endpoints drawn even though it has no current traffic.
 *
 * The node records come from `useDiff`, which fetches the graph for BOTH periods. An earlier
 * version derived them by splitting the id on ":" when the live graph had not seen the node, which
 * is exactly what `contracts/ids.md` §2 forbids: ids are opaque, and parsing one produces
 * confidently wrong labels the moment the format changes. Nothing here parses an id.
 */
function collectNodes(
  diff: DiffResponse,
  known: Map<string, GraphNode>,
): { nodes: GraphNode[]; unresolved: string[] } {
  const ids = new Set<string>();
  for (const edge of diff.edges) {
    ids.add(edge.source_id);
    ids.add(edge.target_id);
  }

  const nodes: GraphNode[] = [];
  const unresolved: string[] = [];
  for (const id of ids) {
    const node = known.get(id);
    if (node) nodes.push(node);
    else unresolved.push(id);
  }

  // An unresolved id should be impossible: the diff and both period graphs are computed from the
  // same two windows with the same filters. There is deliberately no fabricated node for this
  // case — `kind` is a closed set of seven values and inventing an eighth would mean lying to the
  // contract, while reusing a real one would draw something that is simply not true. The edge is
  // dropped and the count surfaced instead, so the gap is visible rather than silently rendered
  // as a plausible wrong node.
  return { nodes, unresolved };
}

interface Props {
  diff: DiffResponse;
  knownNodes: Map<string, GraphNode>;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}

export function CompareCanvas({ diff, knownNodes, selectedId, onSelect }: Props) {
  const cache = useRef<{ positions: PositionCache; signature: string } | undefined>(undefined);

  const { nodes, edges, unresolved } = useMemo(() => {
    const { nodes: graphNodes, unresolved } = collectNodes(diff, knownNodes);
    const drawable = new Set(graphNodes.map((n) => n.id));
    // React Flow drops an edge whose endpoints are missing anyway; filtering explicitly keeps the
    // layout input and the rendered edges consistent.
    const diffEdges = diff.edges.filter(
      (e) => drawable.has(e.source_id) && drawable.has(e.target_id),
    );

    // Reuse the same layout engine as the live view, so switching modes does not rearrange
    // anything that appears in both.
    const asGraphEdges = diffEdges.map((e) => ({
      id: e.id,
      source_id: e.source_id,
      target_id: e.target_id,
      protocol: e.protocol,
      destination_port: e.destination_port,
      connection_count: e.current_connection_count,
      bytes_sent: null,
      bytes_received: null,
      first_seen: diff.baseline.start,
      last_seen: diff.current.end,
    }));

    const result = layoutGraph(graphNodes, asGraphEdges, cache.current);
    cache.current = { positions: result.positions, signature: result.signature };

    const flowNodes: Node<TopologyNodeData>[] = graphNodes.map((node) => ({
      id: node.id,
      type: "topology",
      position: result.positions.get(node.id) ?? { x: 0, y: 0 },
      data: { node, selected: node.id === selectedId },
      draggable: true,
    }));

    const flowEdges: Edge[] = diffEdges.map((edge) => {
      const style = diffStyle(edge);
      return {
        id: edge.id,
        source: edge.source_id,
        target: edge.target_id,
        style: {
          stroke: style.colour,
          strokeWidth: style.width,
          strokeDasharray: style.dash,
        },
        markerEnd: { type: MarkerType.ArrowClosed, color: style.colour, width: 11, height: 11 },
        // The badge is the PRIMARY cue: the classification is spelled out, so the view is
        // readable in greyscale and by a colour-blind reader.
        label: `${style.badge} · ${edge.protocol}:${edge.destination_port}`,
        labelStyle: { fill: "var(--text)", fontSize: 10, fontFamily: "var(--mono)" },
        labelBgStyle: { fill: "var(--ink)", fillOpacity: 0.92 },
        labelBgPadding: [4, 2] as [number, number],
        labelBgBorderRadius: 3,
        animated: false,
      };
    });

    return { nodes: flowNodes, edges: flowEdges, unresolved };
  }, [diff, knownNodes, selectedId]);

  return (
    <>
      {unresolved.length > 0 && (
        // Never expected to appear. If it does, something is wrong with the comparison rather
        // than with the cluster, and saying so beats quietly drawing a smaller graph.
        <p className="banner banner--warn" role="status">
          {unresolved.length} edge endpoint{unresolved.length === 1 ? "" : "s"} could not be
          resolved to a node and {unresolved.length === 1 ? "its edge was" : "their edges were"}{" "}
          omitted.
        </p>
      )}
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={NODE_TYPES}
        onNodeClick={(_, node) => onSelect(node.id)}
        onPaneClick={() => onSelect(null)}
        fitView
        fitViewOptions={{ padding: 0.18, maxZoom: 1.2 }}
        minZoom={0.2}
        maxZoom={2}
      >
        <Background variant={BackgroundVariant.Dots} gap={22} size={1} color="#22303f" />
        <Controls showInteractive={false} />
      </ReactFlow>
    </>
  );
}
