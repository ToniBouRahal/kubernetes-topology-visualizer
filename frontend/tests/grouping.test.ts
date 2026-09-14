import { describe, expect, it } from "vitest";
import type { GraphNode, GraphEdge } from "../src/api/types";
import { groupTopology, focusTopology } from "../src/features/graph/grouping";
const node = (id: string, namespace: string | null): GraphNode => ({ id, namespace, name: id, label: id, kind: namespace ? "Deployment" : "External", first_seen: "2026-01-01T00:00:00Z", last_seen: "2026-01-02T00:00:00Z", attributes: {} });
const edge = (id: string, source_id: string, target_id: string, connection_count = 1, destination_port = 80): GraphEdge => ({ id, source_id, target_id, connection_count, destination_port, protocol: "TCP", bytes_sent: null, bytes_received: null, first_seen: "2026-01-01T00:00:00Z", last_seen: "2026-01-02T00:00:00Z" });
const nodes = [node("a", "web"), node("b", "web"), node("c", "data"), node("outside", null)];
const edges = [edge("ab", "a", "b", 2), edge("ac", "a", "c", 3), edge("bc", "b", "c", 4), edge("ca", "c", "a", 5), edge("co", "c", "outside", 6)];
describe("namespace topology", () => {
  it("aggregates directed traffic while retaining internal connections and external nodes", () => {
    const result = groupTopology(nodes, edges, new Set());
    expect(result.nodes).toHaveLength(3);
    expect(result.edges.map(e => e.connection_count).sort((a,b) => a-b)).toEqual([2,5,6,7]);
    expect(result.groups.size).toBe(2);
    expect(result.nodes.find(n => n.id === "outside")?.kind).toBe("External");
    expect(result.edges.find(e => e.source_id === e.target_id)?.connection_count).toBe(2);
  });
  it("expands namespaces back to their workloads without losing counts", () => {
    const result = groupTopology(nodes, edges, new Set(["web"]));
    expect(result.nodes.map(n => n.id)).toEqual(expect.arrayContaining(["a", "b", "outside"]));
    expect(result.groups.size).toBe(1);
    expect(result.edges.reduce((sum,e) => sum+e.connection_count,0)).toBe(20);
  });
  it("keeps distinct destination ports and does not mutate input", () => {
    const before = JSON.stringify({nodes,edges});
    const result = groupTopology(nodes, [...edges, edge("other", "a", "c", 9, 443)], new Set());
    expect(result.edges.find(e => e.destination_port === 443)?.connection_count).toBe(9);
    expect(JSON.stringify({nodes,edges})).toBe(before);
  });
  it("focuses on incident edges, excluding neighbors' unrelated connections", () => {
    const result = focusTopology(nodes, edges, "a");
    expect(result.nodes.map(n => n.id)).toEqual(["a", "b", "c"]);
    expect(result.edges.map(e => e.id)).toEqual(["ab", "ac", "ca"]);
    expect(focusTopology(nodes, edges, "missing").nodes).toEqual([]);
  });
  it("reduces 1000 workload edges before the rendering budget", () => {
    const many = Array.from({length: 1000}, (_,i) => node(`n${i}`, "web"));
    const result = groupTopology([...many, node("db", "data")], many.map(n => edge(n.id, n.id, "db")), new Set());
    expect(result.nodes).toHaveLength(2);
    expect(result.edges).toHaveLength(1);
    expect(result.edges[0]!.connection_count).toBe(1000);
  });
});

it("aggregates measured failures and weighted setup samples without inventing missing measurements", () => {
  const measured = {...edge("ac", "a", "c", 2), failed_connection_count: 3, connect_latency_count: 2, connect_latency_sum_us: 2000};
  const other = {...edge("bc", "b", "c", 1), failed_connection_count: 0, connect_latency_count: 1, connect_latency_sum_us: 10000};
  const grouped = groupTopology(nodes, [measured, other], new Set()).edges[0]!;
  expect(grouped).toMatchObject({connection_count: 3, failed_connection_count: 3, connect_latency_count: 3, connect_latency_sum_us: 12000});
  expect(groupTopology(nodes, [edge("ac", "a", "c"), edge("bc", "b", "c")], new Set()).edges[0]!.failed_connection_count).toBeNull();
  expect(groupTopology(nodes, [edge("ac", "a", "c"), measured], new Set()).edges[0]!.failed_connection_count).toBe(3);
});
