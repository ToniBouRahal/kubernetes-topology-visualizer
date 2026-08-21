import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  type Edge,
  type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useMemo, useRef } from "react";

import type { GraphEdge, GraphNode, GraphResponse } from "../../api/types";
import { TopologyNode, type TopologyNodeData } from "./TopologyNode";
import { edgeWidth, layoutGraph, type PositionCache } from "./layout";

const NODE_TYPES = { topology: TopologyNode };

interface Props {
  graph: GraphResponse;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}

export function TopologyCanvas({ graph, selectedId, onSelect }: Props) {
  // Positions survive across polls; see layoutGraph for why this matters.
  const cache = useRef<{ positions: PositionCache; signature: string } | undefined>(undefined);

  const { nodes, edges } = useMemo(() => {
    const result = layoutGraph(graph.nodes, graph.edges, cache.current);
    cache.current = { positions: result.positions, signature: result.signature };

    const maxConnections = graph.edges.reduce(
      (max: number, e: GraphEdge) => Math.max(max, e.connection_count),
      0,
    );

    const flowNodes: Node<TopologyNodeData>[] = graph.nodes.map((node: GraphNode) => ({
      id: node.id,
      type: "topology",
      position: result.positions.get(node.id) ?? { x: 0, y: 0 },
      data: { node, selected: node.id === selectedId },
      draggable: true,
    }));

    const flowEdges: Edge[] = graph.edges.map((edge: GraphEdge) => {
      const touchesSelection =
        selectedId !== null && (edge.source_id === selectedId || edge.target_id === selectedId);

      return {
        id: edge.id,
        source: edge.source_id,
        target: edge.target_id,
        // Width encodes connection count on a capped log scale. The legend names the metric,
        // because thickness alone cannot say WHAT is being measured.
        style: {
          stroke: touchesSelection ? "var(--edge-strong)" : "var(--edge)",
          strokeWidth: edgeWidth(edge.connection_count, maxConnections),
        },
        markerEnd: {
          type: MarkerType.ArrowClosed,
          color: touchesSelection ? "var(--edge-strong)" : "var(--edge)",
          width: 11,
          height: 11,
        },
        label: `${edge.protocol}:${edge.destination_port} · ${edge.connection_count}`,
        labelStyle: {
          fill: "var(--text-dim)",
          fontSize: 10,
          fontFamily: "var(--mono)",
        },
        labelBgStyle: { fill: "var(--ink)", fillOpacity: 0.9 },
        labelBgPadding: [4, 2] as [number, number],
        labelBgBorderRadius: 3,
        // No animation: a continuously animated dash on hundreds of edges burns the frame budget
        // the 100 ms polling target depends on (ADR-006 F9).
        animated: false,
      };
    });

    return { nodes: flowNodes, edges: flowEdges };
  }, [graph, selectedId]);

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
      proOptions={{ hideAttribution: false }}
    >
      <Background variant={BackgroundVariant.Dots} gap={22} size={1} color="#22303f" />
      <Controls showInteractive={false} />
    </ReactFlow>
  );
}
