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
 * still needs its endpoints drawn even though they may have no current traffic. They are
 * synthesised here from the ids, using the label the graph endpoint would return.
 */
function synthesiseNodes(diff: DiffResponse, known: Map<string, GraphNode>): GraphNode[] {
  const ids = new Set<string>();
  for (const edge of diff.edges) {
    ids.add(edge.source_id);
    ids.add(edge.target_id);
  }

  return [...ids].map((id) => {
    const existing = known.get(id);
    if (existing) return existing;

    // Fallback for a node that exists only in the baseline period, so the live graph has never
    // seen it. The id is the ONLY source available here; splitting it is confined to this one
    // display-time fallback and never used for identity (contracts/ids.md §2).
    const parts = id.split(":");
    const external = id === "external:EXTERNAL";
    return {
      id,
      kind: (external ? "External" : (parts[3] ?? "Pod")) as GraphNode["kind"],
      namespace: external ? null : (parts[2] ?? null),
      name: external ? "EXTERNAL" : (parts[4] ?? id),
      label: external ? "EXTERNAL" : (parts[4] ?? id),
      first_seen: diff.baseline.start,
      last_seen: diff.current.end,
      attributes: {},
    } satisfies GraphNode;
  });
}

interface Props {
  diff: DiffResponse;
  knownNodes: Map<string, GraphNode>;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}

export function CompareCanvas({ diff, knownNodes, selectedId, onSelect }: Props) {
  const cache = useRef<{ positions: PositionCache; signature: string } | undefined>(undefined);

  const { nodes, edges } = useMemo(() => {
    const graphNodes = synthesiseNodes(diff, knownNodes);

    // Reuse the same layout engine as the live view, so switching modes does not rearrange
    // anything that appears in both.
    const asGraphEdges = diff.edges.map((e) => ({
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

    const flowEdges: Edge[] = diff.edges.map((edge) => {
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

    return { nodes: flowNodes, edges: flowEdges };
  }, [diff, knownNodes, selectedId]);

  return (
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
  );
}
