import {
  BaseEdge,
  type EdgeProps,
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  type Edge,
  type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useEffect, useMemo, useRef, useState } from "react";

import type { GraphEdge, GraphNode, GraphResponse } from "../../api/types";
import { groupTopology, focusTopology } from "./grouping";
import { applyRenderBudget, MAX_RENDERED_EDGES } from "./renderBudget";
import { TopologyNode, type TopologyNodeData } from "./TopologyNode";
import { edgeWidth, layoutGraph, type PositionCache } from "./layout";

import { outcomeLabel } from "./outcomes";

const NODE_TYPES = { topology: TopologyNode };

function NamespaceLoop({ id, sourceX, sourceY, targetX, targetY, markerEnd, style, label, labelStyle, labelBgStyle, data }: EdgeProps) {
  const rise = 90 + Number(data?.loopIndex ?? 0) * 32;
  const top = Math.min(sourceY, targetY) - rise;
  const path = `M ${sourceX} ${sourceY} C ${sourceX + 80} ${top}, ${targetX - 80} ${top}, ${targetX} ${targetY}`;
  return <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style}
    label={label} labelX={(sourceX + targetX) / 2} labelY={(sourceY + targetY) / 2 - rise * 0.75}
    labelStyle={labelStyle} labelBgStyle={labelBgStyle} />;
}
const EDGE_TYPES = { namespaceLoop: NamespaceLoop };

interface Props {
  graph: GraphResponse;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  /** Told what was dropped, so the banner can say so outside the canvas. */
  onBudget?: (capped: { shownEdges: number; totalEdges: number; hiddenNodes: number } | null) => void;
}

export function TopologyCanvas({ graph, selectedId, onSelect, onBudget }: Props) {
  const [grouped, setGrouped] = useState(graph.edges.length > MAX_RENDERED_EDGES);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [focusedId, setFocusedId] = useState<string | null>(null);
  const focusNode = graph.nodes.find(n => n.id === focusedId);
  const activeFocus = focusNode?.id ?? null;
  const view = useMemo(() => {
    if (activeFocus) return { ...focusTopology(graph.nodes, graph.edges, activeFocus), groups: new Map() };
    if (grouped) return groupTopology(graph.nodes, graph.edges, expanded);
    return { nodes: graph.nodes, edges: graph.edges, groups: new Map() };
  }, [graph, grouped, expanded, activeFocus]);
  const viewKey = JSON.stringify([grouped, [...expanded].sort(), activeFocus]);
  // Positions survive across polls; see layoutGraph for why this matters.
  const cache = useRef<{ positions: PositionCache; signature: string } | undefined>(undefined);

  const { nodes, edges, capped } = useMemo(() => {
    // Cap BEFORE layout. Past roughly 300 edges React Flow stops responding altogether
    // (docs/limitations.md §4.1), and laying out a graph that will never paint wastes the work
    // twice over.
    const budget = applyRenderBudget(view.nodes, view.edges);

    const result = layoutGraph(budget.nodes, budget.edges, cache.current);
    cache.current = { positions: result.positions, signature: result.signature };

    const maxConnections = budget.edges.reduce(
      (max: number, e: GraphEdge) => Math.max(max, e.connection_count),
      0,
    );

    const flowNodes: Node<TopologyNodeData>[] = budget.nodes.map((node: GraphNode) => ({
      id: node.id,
      type: "topology",
      position: result.positions.get(node.id) ?? { x: 0, y: 0 },
      data: { node, selected: node.id === selectedId, group: view.groups.get(node.id) },
      draggable: true,
    }));

    const loopCounts = new Map<string, number>();
    const flowEdges: Edge[] = budget.edges.map((edge: GraphEdge) => {
      const touchesSelection =
        selectedId !== null && (edge.source_id === selectedId || edge.target_id === selectedId);

      const loopIndex = loopCounts.get(edge.source_id) ?? 0;
      if (edge.source_id === edge.target_id) loopCounts.set(edge.source_id, loopIndex + 1);
      const hasFailures = (edge.failed_connection_count ?? 0) > 0;
      const stroke = hasFailures ? "var(--warn)" : touchesSelection ? "var(--edge-strong)" : "var(--edge)";
      return {
        data: { loopIndex },
        id: edge.id,
        type: edge.source_id === edge.target_id ? "namespaceLoop" : "default",
        source: edge.source_id,
        target: edge.target_id,
        // Width encodes connection count on a capped log scale. The legend names the metric,
        // because thickness alone cannot say WHAT is being measured.
        style: {
          stroke,
          strokeDasharray: hasFailures ? "6 4" : undefined,
          strokeWidth: edgeWidth(edge.connection_count, maxConnections),
        },
        markerEnd: {
          type: MarkerType.ArrowClosed,
          color: stroke,
          width: 11,
          height: 11,
        },
        label: `${edge.protocol}:${edge.destination_port} · ${outcomeLabel(edge)}`,
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

    return { nodes: flowNodes, edges: flowEdges, capped: budget.capped };
  }, [view, selectedId]);

  // Reported through an effect, not during render: calling a parent's setState mid-render is what
  // React warns about, and the banner lives outside the canvas so it is not clipped by the pane.
  useEffect(() => {
    onBudget?.(capped);
  }, [capped, onBudget]);

  return (
    <>
    <div className="topology-toolbar" aria-label="Topology view controls">
      <button type="button" aria-pressed={grouped && !activeFocus} onClick={() => { setGrouped(true); setFocusedId(null); cache.current = undefined; }}>Namespaces</button>
      <button type="button" aria-pressed={!grouped && !activeFocus} onClick={() => { setGrouped(false); setFocusedId(null); cache.current = undefined; }}>Workloads</button>
      <button type="button" disabled={!selectedId || !graph.nodes.some(n => n.id === selectedId)} onClick={() => { setFocusedId(selectedId); cache.current = undefined; }}>Focus selected workload</button>
      {activeFocus && <><span>Direct neighbors of {focusNode?.label}</span><button type="button" onClick={() => { setFocusedId(null); cache.current = undefined; }}>Exit focus</button></>}
      {grouped && !activeFocus && expanded.size > 0 && <button type="button" onClick={() => { setExpanded(new Set()); cache.current = undefined; }}>Collapse all</button>}
      {grouped && !activeFocus && [...expanded].filter(ns => graph.nodes.some(n => n.namespace === ns)).sort().map(ns => <button type="button" key={ns} onClick={() => { setExpanded(current => { const next = new Set(current); next.delete(ns); return next; }); cache.current = undefined; }}>Collapse {ns}</button>)}
      <span>{view.nodes.length} nodes · {view.edges.length} relationships before display limit</span>
    </div>
    <ReactFlow
      key={viewKey}
      nodes={nodes}
      edges={edges}
      nodeTypes={NODE_TYPES}
      edgeTypes={EDGE_TYPES}
      onNodeClick={(_, node) => {
        const group = view.groups.get(node.id);
        if (group) {
          setExpanded(current => new Set([...current, group.namespace]));
          cache.current = undefined;
        } else onSelect(node.id);
      }}
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
    </>
  );
}
