import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GraphResponse } from "../src/api/types";
import { TopologyCanvas } from "../src/features/graph/TopologyCanvas";
// jsdom has no measured canvas. Replace the rendering boundary, keeping real view controls,
// grouping, layout and budget logic so interactions exercise the production data flow.
vi.mock("@xyflow/react", () => ({
  ReactFlow: ({nodes, edges, onNodeClick}: {nodes: {id: string; data: {node: {label: string}}}[]; edges: {id: string; label: string; style: {stroke: string; strokeDasharray?: string}}[]; onNodeClick: (event: null, node: {id: string}) => void}) => <div><span>{edges.length} drawn edges</span>{edges.map(e => <span key={e.id} data-testid="edge" style={e.style}>{e.label}</span>)}{nodes.map(n => <button key={n.id} onClick={() => onNodeClick(null, n)}>{n.data.node.label}</button>)}</div>,
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

it("labels failed-only connections and draws a dashed warning", () => {
  render(<TopologyCanvas graph={{...graph, edges: [{...graph.edges[0]!, connection_count: 0, failed_connection_count: 3}]}} selectedId={null} onSelect={() => {}}/>);
  expect(screen.getByTestId("edge")).toHaveTextContent("0 successful · 3 failed/aborted");
  expect(screen.getByTestId("edge").style.strokeDasharray).toBe("6 4");
  expect(screen.getByTestId("edge").style.stroke).toBe("var(--warn)");
});
it("distinguishes unmeasured outcomes from measured zero and names TCP setup timing", () => {
  const {rerender} = render(<TopologyCanvas graph={graph} selectedId={null} onSelect={() => {}}/>);
  expect(screen.getAllByTestId("edge")[0]).toHaveTextContent("failed/aborted unmeasured");
  expect(screen.getAllByTestId("edge")[0]).not.toHaveTextContent("mean TCP setup");
  rerender(<TopologyCanvas graph={{...graph, edges: [{...graph.edges[0]!, failed_connection_count: 0, connect_latency_count: 1, connect_latency_sum_us: 0}]}} selectedId={null} onSelect={() => {}}/>);
  expect(screen.getByTestId("edge")).toHaveTextContent("0 failed/aborted · mean TCP setup 0 ms");
});

it("labels namespace setup timing using weighted samples", () => {
  render(<TopologyCanvas graph={{...graph, edges: [
    {...graph.edges[1]!, connection_count: 2, connect_latency_count: 2, connect_latency_sum_us: 2000},
    {...graph.edges[1]!, id: "second", source_id: "a", connection_count: 1, connect_latency_count: 1, connect_latency_sum_us: 10000},
  ]}} selectedId={null} onSelect={() => {}}/>);
  fireEvent.click(screen.getByRole("button", {name: "Namespaces"}));
  expect(screen.getByTestId("edge")).toHaveTextContent("3 successful · failed/aborted unmeasured · mean TCP setup 4 ms");
});
