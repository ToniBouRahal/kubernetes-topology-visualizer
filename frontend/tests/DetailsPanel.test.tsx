import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { DetailsPanel } from "../src/features/details/DetailsPanel";
import type { GraphNode, NodeDetail } from "../src/api/types";
afterEach(cleanup);
it("shows successful setup timing and failed-only dependencies with outcome words", () => {
  const node = {id: "a", name: "a", label: "a", namespace: "web", kind: "Deployment", first_seen: "2026-01-01", last_seen: "2026-01-02", attributes: {}} as GraphNode;
  const detail: NodeDetail = {node, window: {start: node.first_seen, end: node.last_seen}, incoming: [], outgoing: [
    {first_seen: node.first_seen, last_seen: node.last_seen, node_id: "b", label: "b", protocol: "TCP", destination_port: 80, connection_count: 3, failed_connection_count: 0, connect_latency_count: 3, connect_latency_sum_us: 12000},
    {first_seen: node.first_seen, last_seen: node.last_seen, node_id: "c", label: "c", protocol: "TCP", destination_port: 443, connection_count: 0, failed_connection_count: 2},
  ]};
  render(<DetailsPanel node={node} detail={detail} loading={false} onClose={() => {}}/>);
  expect(screen.getByText(/3 successful/)).toHaveTextContent("0 failed/aborted · mean TCP setup 4 ms");
  expect(screen.getByText(/0 successful/)).toHaveTextContent("2 failed/aborted");
});
