import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GraphResponse } from "../src/api/types";
import { TopologyCanvas } from "../src/features/graph/TopologyCanvas";
import { TopologyNode } from "../src/features/graph/TopologyNode";
import type { GraphNode } from "../src/api/types";
// jsdom has no measured canvas. Replace the rendering boundary, keeping real view controls,
// grouping, layout and budget logic so interactions exercise the production data flow.
vi.mock("@xyflow/react", () => ({
  // Dimming is CSS (app.css): on a focused canvas, anything not marked topology-focus recedes. The
  // mock applies the same rule so the assertions below read what a user would see.
  ReactFlow: ({nodes, edges, onNodeClick, className}: {className?: string; nodes: {id: string; className?: string; data: {node: {label: string}; degree?: number}}[]; edges: {id: string; className?: string; label: string; markerEnd?: {type: string}; style: {stroke: string; strokeDasharray?: string}}[]; onNodeClick: (event: null, node: {id: string}) => void}) => {
    const recedes = (c?: string) => Boolean(className?.includes("topology-canvas--focused")) && !c?.includes("topology-focus");
    return <div><span>{edges.length} drawn edges</span>{edges.map(e => <span key={e.id} data-testid="edge" data-marker={e.markerEnd?.type} data-opacity={recedes(e.className) ? 0.16 : 1} style={e.style}>{e.label}</span>)}{nodes.map(n => <button key={n.id} data-testid="node" data-dimmed={String(recedes(n.className))} data-degree={n.data.degree} onClick={() => onNodeClick(null, n)}>{n.data.node.label}</button>)}</div>;
  },
  BaseEdge: () => null, Background: () => null, Controls: () => null, Handle: () => null,
  BackgroundVariant: {Dots: "dots"}, MarkerType: {ArrowClosed: "arrow"}, Position: {},
}));
afterEach(cleanup);
const nodes = ["a", "b", "c"].map((id,i) => ({id, name: id, label: id, namespace: i < 2 ? "web" : "db", kind: "Deployment", attributes: {}, first_seen: "2026-01-01", last_seen: "2026-01-02"}));
const edges = [["a","b"], ["b","c"]].map(([source_id,target_id],i) => ({id: String(i),source_id,target_id,connection_count: 1,destination_port: 80,protocol: "TCP"}));
const graph = {nodes,edges} as GraphResponse;
describe("topology controls", () => {
  it("expands and collapses a namespace without selecting a synthetic API node", () => {
    const select = vi.fn();
    render(<TopologyCanvas graph={graph} selectedId={null} onSelect={select}/>);
    fireEvent.click(screen.getByRole("button", {name: "Namespaces"}));
    expect(screen.queryByRole("button", {name: "a"})).toBeNull();
    fireEvent.click(screen.getByRole("button", {name: "web"}));
    expect(screen.getByRole("button", {name: "a"})).toBeInTheDocument();
    expect(select).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", {name: "Collapse web"}));
    expect(screen.getByRole("button", {name: "web"})).toBeInTheDocument();
  });
  it("focuses a selected workload and restores the full view on exit", () => {
    render(<TopologyCanvas graph={graph} selectedId="a" onSelect={() => {}}/>);
    fireEvent.click(screen.getByRole("button", {name: "Focus selected workload"}));
    expect(screen.queryByRole("button", {name: "c"})).toBeNull();
    expect(screen.getByText("1 drawn edges")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", {name: "Exit focus"}));
    expect(screen.getByRole("button", {name: "c"})).toBeInTheDocument();
  });
});

/*
 * Edge labels since ADR-010 D-10.6.
 *
 * The full reading is ~350px of text on every edge, which buried the graph under its own labels,
 * so it is now shown for the SELECTED component's neighbourhood and the port alone is shown
 * otherwise. The readings below are therefore asserted with a selection in place — that is where
 * the interface now states them, and the property being protected is unchanged: an unmeasured
 * outcome must never be presentable as a measured zero.
 */
it("shows the port alone until a component is selected", () => {
  render(<TopologyCanvas graph={graph} selectedId={null} onSelect={() => {}}/>);
  expect(screen.getAllByTestId("edge")[0]).toHaveTextContent("TCP:80");
  // The counts are one click away, not on screen by default.
  expect(screen.getAllByTestId("edge")[0]).not.toHaveTextContent("successful");
});

it("drops the label entirely for edges outside the selected neighbourhood", () => {
  // a→b touches the selection; b→c does not. A faded edge keeping a legible label would say the
  // link was in focus when it is not.
  render(<TopologyCanvas graph={graph} selectedId="a" onSelect={() => {}}/>);
  const labels = screen.getAllByTestId("edge").map(e => e.textContent);
  expect(labels.filter(t => t !== "")).toHaveLength(1);
});

it("names a node for a screen reader without naming its kind — T-10.2", () => {
  // A screen reader sees neither the fill nor the diameter, so both encodings are restated in
  // words. Kind is absent here for the same reason it is absent on the canvas.
  render(<TopologyNode data={{node: nodes[0]! as GraphNode, degree: 2}}/>);
  const named = screen.getByLabelText("a in web, talks to 2 components");
  expect(named).toBeInTheDocument();
  expect(named).not.toHaveTextContent("Deployment");
});

it("gives every edge a destination arrowhead — T-10.5", () => {
  // A curve is not self-evidently directed, and a dependency graph that does not say which way
  // the dependency runs answers nothing. ADR-010 D-10.5 restates this precisely because the
  // redesign touched every other edge property.
  render(<TopologyCanvas graph={graph} selectedId={null} onSelect={() => {}}/>);
  for (const edge of screen.getAllByTestId("edge")) {
    expect(edge.dataset.marker).toBe("arrow");
  }
});

describe("selecting a component focuses its neighbourhood (D-10.6)", () => {
  // a→b→c. Selecting b puts a and c in the neighbourhood; selecting a leaves c outside it.
  it("dims every node that is neither the selection nor a direct neighbour — T-10.6", () => {
    render(<TopologyCanvas graph={graph} selectedId="a" onSelect={() => {}}/>);
    const dimmed = (label: string) =>
      screen.getAllByTestId("node").find(n => n.textContent === label)!.dataset.dimmed;
    expect(dimmed("a")).toBe("false");
    expect(dimmed("b")).toBe("false");
    expect(dimmed("c")).toBe("true");
  });

  it("dims every edge that does not touch the selection — T-10.7", () => {
    render(<TopologyCanvas graph={graph} selectedId="a" onSelect={() => {}}/>);
    const opacities = screen.getAllByTestId("edge").map(e => Number(e.dataset.opacity));
    // a→b touches it and stays fully drawn; b→c runs between two nodes in the neighbourhood but
    // is not part of it, which is the case that is easy to get wrong.
    expect(opacities).toContain(1);
    expect(opacities.filter(o => o < 1)).toHaveLength(1);
  });

  it("leaves the whole graph lit when nothing is selected", () => {
    render(<TopologyCanvas graph={graph} selectedId={null} onSelect={() => {}}/>);
    expect(screen.getAllByTestId("node").every(n => n.dataset.dimmed === "false")).toBe(true);
    expect(screen.getAllByTestId("edge").every(e => Number(e.dataset.opacity) === 1)).toBe(true);
  });
});

it("labels failed-only connections and draws a dashed warning", () => {
  render(<TopologyCanvas graph={{...graph, edges: [{...graph.edges[0]!, connection_count: 0, failed_connection_count: 3}]}} selectedId="a" onSelect={() => {}}/>);
  expect(screen.getByTestId("edge")).toHaveTextContent("0 successful · 3 failed/aborted");
  expect(screen.getByTestId("edge").style.strokeDasharray).toBe("6 4");
  expect(screen.getByTestId("edge").style.stroke).toBe("var(--warn)");
});
it("distinguishes unmeasured outcomes from measured zero and names TCP setup timing", () => {
  const {rerender} = render(<TopologyCanvas graph={graph} selectedId="a" onSelect={() => {}}/>);
  expect(screen.getAllByTestId("edge")[0]).toHaveTextContent("failed/aborted unmeasured");
  expect(screen.getAllByTestId("edge")[0]).not.toHaveTextContent("mean TCP setup");
  rerender(<TopologyCanvas graph={{...graph, edges: [{...graph.edges[0]!, failed_connection_count: 0, connect_latency_count: 1, connect_latency_sum_us: 0}]}} selectedId="a" onSelect={() => {}}/>);
  expect(screen.getByTestId("edge")).toHaveTextContent("0 failed/aborted · mean TCP setup 0 ms");
});

it("labels namespace setup timing using weighted samples", () => {
  render(<TopologyCanvas graph={{...graph, edges: [
    {...graph.edges[1]!, connection_count: 2, connect_latency_count: 2, connect_latency_sum_us: 2000},
    {...graph.edges[1]!, id: "second", source_id: "a", connection_count: 1, connect_latency_count: 1, connect_latency_sum_us: 10000},
  ]}} selectedId={'namespace:"web"'} onSelect={() => {}}/>);
  fireEvent.click(screen.getByRole("button", {name: "Namespaces"}));
  expect(screen.getByTestId("edge")).toHaveTextContent("3 successful · failed/aborted unmeasured · mean TCP setup 4 ms");
});
