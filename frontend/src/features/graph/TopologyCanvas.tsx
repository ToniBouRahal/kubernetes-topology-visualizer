import {
  BaseEdge,
  type EdgeProps,
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  useReactFlow,
  useStore,
  type Edge,
  type FitViewOptions,
  type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useEffect, useMemo, useRef, useState } from "react";

import type { GraphEdge, GraphNode, GraphResponse } from "../../api/types";
import { groupTopology, focusTopology } from "./grouping";
import { applyRenderBudget } from "./renderBudget";
import { TopologyNode, type TopologyNodeData } from "./TopologyNode";
import { degreesOf, edgeWidth, layoutGraph, nodeDiameter, type PositionCache } from "./layout";

import { FloatingEdge } from "./FloatingEdge";
import { edgeBends, routeBends, type Disc } from "./edgeGeometry";

const NODE_TYPES = { topology: TopologyNode };

function NamespaceLoop({ id, sourceX, sourceY, targetX, targetY, markerEnd, style, data }: EdgeProps) {
  const rise = 90 + Number(data?.loopIndex ?? 0) * 32;
  const top = Math.min(sourceY, targetY) - rise;
  const path = `M ${sourceX} ${sourceY} C ${sourceX + 80} ${top}, ${targetX - 80} ${top}, ${targetX} ${targetY}`;
  return <BaseEdge id={id} path={path} markerEnd={markerEnd} style={style} />;
}
const EDGE_TYPES = { namespaceLoop: NamespaceLoop, floating: FloatingEdge };

/**
 * Hoisted so its identity is stable. React Flow re-reads this prop, and a fresh object literal
 * on every render means a new identity on every poll.
 */
const FIT_OPTIONS: FitViewOptions = {
  // The top margin clears the view-controls toolbar, which floats over the top of the pane.
  padding: { top: "72px", right: "8%", bottom: "8%", left: "8%" },
  maxZoom: 1.2,
};

/**
 * Past this many edges the canvas opens grouped by namespace. Readability, not performance: the
 * per-workload view draws up to MAX_RENDERED_EDGES responsively, but at that density a reader
 * needs the namespaces first and the workloads on request.
 */
const GROUP_BY_DEFAULT_EDGES = 400;

/** Marks a node or edge in the selected neighbourhood, which stays lit while the rest recedes. */
const FOCUS_CLASS = "topology-focus";

interface Reusable<T> {
  key: string;
  value: T;
}

/** The previous object for `id` when `key` says it draws the same thing, else a new one. */
function reuse<T>(previous: Map<string, Reusable<T>>, next: Map<string, Reusable<T>>, id: string, key: string, make: () => T): T {
  const old = previous.get(id);
  const entry = old && old.key === key ? old : { key, value: make() };
  next.set(id, entry);
  return entry.value;
}

/**
 * Re-fits the view when the pane changes size.
 *
 * React Flow's own `fitView` prop queues a fit that runs once the nodes are measured, and it
 * gets the first paint right — measured at 1280x720, all eight demo components land inside the
 * pane. What it does not do is run again: the fit is mount-only, so narrowing the window keeps
 * the old viewport and pushes components outside it with nothing on screen to say they are
 * missing. Dragging a window narrower is exactly what happens when someone shares a screen.
 *
 * This lives in a child component because `useReactFlow` only resolves inside the <ReactFlow>
 * tree. It deliberately does NOT gate on `useNodesInitialized`: that hook stays false for this
 * canvas even after the nodes are measured and the graph is interactive (verified in a browser),
 * so gating on it silently disables the observer. The pane cannot resize before it exists, which
 * is the only ordering this actually needs.
 */
function FitToPane() {
  const { fitView } = useReactFlow();
  const pane = useStore((state) => state.domNode);

  useEffect(() => {
    if (!pane) return;
    // Debounced: a drag-resize fires this continuously, and re-solving the viewport every frame
    // makes the graph swim under the cursor.
    let timer: number | undefined;
    let first = true;
    const observer = new ResizeObserver(() => {
      // ResizeObserver fires once on observe. That first call is the size the mount-time fit
      // already solved for, so acting on it would re-fit the view for no reason.
      if (first) {
        first = false;
        return;
      }
      window.clearTimeout(timer);
      timer = window.setTimeout(() => void fitView(FIT_OPTIONS), 150);
    });
    observer.observe(pane);
    return () => {
      window.clearTimeout(timer);
      observer.disconnect();
    };
  }, [pane, fitView]);

  return null;
}

interface Props {
  graph: GraphResponse;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  /** Told what was dropped, so the banner can say so outside the canvas. */
  onBudget?: (capped: { shownEdges: number; totalEdges: number; hiddenNodes: number } | null) => void;
}

export function TopologyCanvas({ graph, selectedId, onSelect, onBudget }: Props) {
  const [grouped, setGrouped] = useState(graph.edges.length > GROUP_BY_DEFAULT_EDGES);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [focusedId, setFocusedId] = useState<string | null>(null);
  const focusNode = graph.nodes.find(n => n.id === focusedId);
  const activeFocus = focusNode?.id ?? null;
  const view = useMemo(() => {
    if (activeFocus) return { ...focusTopology(graph.nodes, graph.edges, activeFocus), groups: new Map() };
    if (grouped) return groupTopology(graph.nodes, graph.edges, expanded);
    return { nodes: graph.nodes, edges: graph.edges, groups: new Map() };
  }, [graph, grouped, expanded, activeFocus]);
  // The filters the backend applied to THIS response. Keyed on the response, not on the filter
  // controls, so the change lands when the filtered graph arrives rather than one request early —
  // re-fitting the old graph and then leaving the new one where it fell.
  const filterKey = JSON.stringify(graph.filters ?? null);
  // A new key remounts the canvas, and React Flow fits the view on mount. That happens on a view
  // toggle and on a filter change — both the reader's own doing — and never on a poll, so a
  // workload appearing does not throw away the zoom someone is reading at.
  const viewKey = JSON.stringify([grouped, [...expanded].sort(), activeFocus, filterKey]);
  // Positions survive across polls; see layoutGraph for why this matters.
  const cache = useRef<{ positions: PositionCache; signature: string } | undefined>(undefined);
  const laidOutFor = useRef(filterKey);
  const routes = useRef<{ positions: PositionCache; discs: Map<string, Disc>; bends: Map<string, number> } | undefined>(undefined);

  // What gets drawn and where. Selection and hover do not change this, so clicking a component
  // never re-runs the budget or the layout.
  const drawn = useMemo(() => {
    // A filter change is a different question, not the same graph moving on: lay the answer out
    // fresh. Keeping the survivors where the unfiltered layout put them leaves a few nodes
    // scattered across the space the rest used to fill.
    if (laidOutFor.current !== filterKey) {
      cache.current = undefined;
      laidOutFor.current = filterKey;
    }

    // Cap BEFORE layout: laying out a graph that will never paint wastes the work twice over.
    const budget = applyRenderBudget(view.nodes, view.edges);

    // Degree sizes the nodes (D-10.3) and is computed from what will actually be DRAWN — a node
    // whose neighbours were dropped by the render budget must not claim them.
    const degrees = degreesOf(budget.nodes, budget.edges);

    const result = layoutGraph(budget.nodes, budget.edges, cache.current, degrees);
    cache.current = { positions: result.positions, signature: result.signature };

    const maxConnections = budget.edges.reduce(
      (max: number, e: GraphEdge) => Math.max(max, e.connection_count),
      0,
    );
    // Curves depend only on where things are, so they are worked out when the layout is, and a
    // poll that reused the cached layout (the same positions object) reuses the curves too.
    // Routing checks every edge against every node; doing that per poll cost ~70 ms at 2,000 edges.
    let geometry = routes.current?.positions === result.positions ? routes.current : undefined;
    if (!geometry) {
      const discs = new Map<string, Disc>();
      for (const node of budget.nodes) {
        const p = result.positions.get(node.id);
        if (!p) continue;
        const r = nodeDiameter(degrees.get(node.id) ?? 0) / 2;
        discs.set(node.id, { x: p.x + r, y: p.y + r, r });
      }
      geometry = { positions: result.positions, discs, bends: routeBends(budget.edges, discs, edgeBends(budget.edges)) };
      routes.current = geometry;
    }
    const { discs, bends } = geometry;

    return { budget, degrees, positions: result.positions, maxConnections, bends, discs };
  }, [view, filterKey]);

  // Flow objects from the previous render, keyed by what they draw. React Flow re-renders an edge
  // or node only when its object changes identity, so handing back the same object for anything
  // that looks the same is what keeps a click or a poll from redrawing the whole graph.
  const reused = useRef({ nodes: new Map<string, Reusable<Node<TopologyNodeData>>>(), edges: new Map<string, Reusable<Edge>>() });

  const { nodes, edges } = useMemo(() => {
    const { budget, degrees, positions, maxConnections, bends, discs } = drawn;
    const nextNodes = new Map<string, Reusable<Node<TopologyNodeData>>>();
    const nextEdges = new Map<string, Reusable<Edge>>();

    // Selecting a component focuses its neighbourhood (D-10.6): it and what it talks to stay lit,
    // everything else recedes. Membership is decided here, once, so nodes and edges cannot
    // disagree about who is in the neighbourhood.
    const neighbours = new Set<string>();
    if (selectedId !== null) {
      for (const edge of budget.edges) {
        if (edge.source_id === selectedId) neighbours.add(edge.target_id);
        if (edge.target_id === selectedId) neighbours.add(edge.source_id);
      }
    }
    // Only the neighbourhood is marked; everything unmarked recedes through CSS on the canvas
    // (.topology-canvas--focused in app.css). Marking what stays lit rather than what dims means
    // a click changes the handful of objects around the selection, not every one in the graph.
    const inFocus = (id: string) => selectedId !== null && (id === selectedId || neighbours.has(id));

    const flowNodes: Node<TopologyNodeData>[] = budget.nodes.map((node: GraphNode) => {
      const position = positions.get(node.id) ?? { x: 0, y: 0 };
      const group = view.groups.get(node.id);
      const degree = degrees.get(node.id) ?? 0;
      const selected = node.id === selectedId;
      const focus = inFocus(node.id);
      // Everything TopologyNode draws. A reused object may carry an older GraphNode whose
      // timestamps differ; nothing on the canvas reads them, and the details panel reads its own.
      const key = [position.x, position.y, node.label, node.name, node.namespace, node.kind,
        group?.namespace, group?.workloads, degree, selected, focus].join("|");
      return reuse(reused.current.nodes, nextNodes, node.id, key, () => ({
        id: node.id,
        type: "topology",
        position,
        data: { node, selected, group, degree },
        className: focus ? FOCUS_CLASS : undefined,
        draggable: true,
      }));
    });

    // No text on the canvas's edges, selected or not: the port and every count are in the details
    // panel, one click away. A screen reader still gets the port, from the edge's aria-label.
    const names = new Map(budget.nodes.map((n: GraphNode) => [n.id, n.label]));
    const loopCounts = new Map<string, number>();
    const flowEdges: Edge[] = budget.edges.map((edge: GraphEdge) => {
      const touchesSelection =
        selectedId !== null && (edge.source_id === selectedId || edge.target_id === selectedId);

      const loopIndex = loopCounts.get(edge.source_id) ?? 0;
      if (edge.source_id === edge.target_id) loopCounts.set(edge.source_id, loopIndex + 1);
      const hasFailures = (edge.failed_connection_count ?? 0) > 0;
      const stroke = hasFailures ? "var(--warn)" : touchesSelection ? "var(--edge-strong)" : "var(--edge)";
      const width = edgeWidth(edge.connection_count, maxConnections);
      const ariaLabel = `${names.get(edge.source_id) ?? edge.source_id} to ${names.get(edge.target_id) ?? edge.target_id}, ${edge.protocol} port ${edge.destination_port}`;

      const bend = bends.get(edge.id) ?? 0;
      const from = discs.get(edge.source_id);
      const to = discs.get(edge.target_id);
      const key = [edge.source_id, edge.target_id, loopIndex, bend, from?.x, from?.y, from?.r, to?.x, to?.y, to?.r,
        stroke, hasFailures, width, touchesSelection, ariaLabel].join("|");
      return reuse(reused.current.edges, nextEdges, edge.id, key, () => ({
        data: { loopIndex, bend, from, to },
        id: edge.id,
        type: edge.source_id === edge.target_id ? "namespaceLoop" : "floating",
        source: edge.source_id,
        target: edge.target_id,
        className: touchesSelection ? FOCUS_CLASS : undefined,
        ariaLabel,
        // Width encodes connection count on a capped log scale. The legend names the metric,
        // because thickness alone cannot say WHAT is being measured.
        style: {
          stroke,
          strokeDasharray: hasFailures ? "6 4" : undefined,
          strokeWidth: width,
        },
        markerEnd: {
          type: MarkerType.ArrowClosed,
          color: stroke,
          width: 11,
          height: 11,
        },
        // No animation: a continuously animated dash on hundreds of edges burns the frame budget
        // the 100 ms polling target depends on (ADR-006 F9).
        animated: false,
      }));
    });

    reused.current = { nodes: nextNodes, edges: nextEdges };
    return { nodes: flowNodes, edges: flowEdges };
  }, [drawn, view.groups, selectedId]);
  const capped = drawn.budget.capped;

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
      <span>{view.nodes.length} components · {view.edges.length} links before display limit</span>
    </div>
    <ReactFlow
      key={viewKey}
      className={selectedId !== null ? "topology-canvas--focused" : undefined}
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
      // Off-screen nodes and edges are not mounted. At fit-view every one is on screen, so this
      // pays off once someone zooms in to read a neighbourhood.
      onlyRenderVisibleElements
      fitView
      fitViewOptions={FIT_OPTIONS}
      minZoom={0.2}
      maxZoom={2}
      proOptions={{ hideAttribution: false }}
    >
      {/* Ruled like a blueprint rather than dotted. Literal hex, not a token: React Flow writes
          this into an SVG pattern attribute, where a var() reference does not resolve. Mirrors
          --line in tokens.css — change both together. */}
      <Background variant={BackgroundVariant.Lines} gap={40} size={1} color="#dbe3ea" />
      <Controls showInteractive={false} />
      <FitToPane />
    </ReactFlow>
    </>
  );
}
