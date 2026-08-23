/**
 * The render budget — P5-F18, docs/limitations.md §4.1.
 *
 * The canvas stops responding past roughly 300 edges rather than slowing down, so the alternative
 * to capping is a locked browser tab. These tests pin the properties that make the cap honest:
 * it keeps the busiest traffic, it never leaves a node stranded without an edge, and it reports
 * exactly what it dropped.
 */
import { describe, expect, it } from "vitest";

import type { GraphEdge, GraphNode } from "../src/api/types";
import { applyRenderBudget, MAX_RENDERED_EDGES } from "../src/features/graph/renderBudget";

function node(id: string): GraphNode {
  return {
    id,
    kind: "Deployment",
    namespace: "demo",
    name: id,
    label: id,
    first_seen: "2026-08-22T12:00:00Z",
    last_seen: "2026-08-22T12:05:00Z",
    attributes: {},
  } as GraphNode;
}

function edge(id: string, source: string, target: string, count: number): GraphEdge {
  return {
    id,
    source_id: source,
    target_id: target,
    protocol: "TCP",
    destination_port: 8080,
    connection_count: count,
    bytes_sent: null,
    bytes_received: null,
    first_seen: "2026-08-22T12:00:00Z",
    last_seen: "2026-08-22T12:05:00Z",
  } as GraphEdge;
}

describe("applyRenderBudget", () => {
  it("leaves a graph under the limit completely untouched", () => {
    const nodes = [node("a"), node("b")];
    const edges = [edge("e1", "a", "b", 5)];
    const result = applyRenderBudget(nodes, edges, 400);

    expect(result.capped).toBeNull();
    expect(result.edges).toBe(edges);
    expect(result.nodes).toBe(nodes);
  });

  it("keeps exactly the limit when the graph is one over", () => {
    const nodes = Array.from({ length: 12 }, (_, i) => node(`n${i}`));
    const edges = Array.from({ length: 11 }, (_, i) => edge(`e${i}`, "n0", `n${i + 1}`, i + 1));

    const result = applyRenderBudget(nodes, edges, 10);
    expect(result.edges).toHaveLength(10);
    expect(result.capped).toEqual({ shownEdges: 10, totalEdges: 11, hiddenNodes: 1 });
  });

  it("keeps the BUSIEST edges, not an arbitrary slice", () => {
    // The property that matters: an arbitrary subset would look like a complete graph while
    // hiding whichever relationships happened to sort last.
    const nodes = Array.from({ length: 6 }, (_, i) => node(`n${i}`));
    const edges = [
      edge("quiet-1", "n0", "n1", 1),
      edge("busiest", "n0", "n2", 9999),
      edge("quiet-2", "n0", "n3", 2),
      edge("second", "n0", "n4", 500),
      edge("quiet-3", "n0", "n5", 3),
    ];

    const result = applyRenderBudget(nodes, edges, 2);
    expect(result.edges.map((e) => e.id)).toEqual(["busiest", "second"]);
  });

  it("drops nodes left with no edge, because a node only exists to carry one", () => {
    const nodes = [node("a"), node("b"), node("orphan")];
    const edges = [edge("keep", "a", "b", 100), edge("drop", "a", "orphan", 1)];

    const result = applyRenderBudget(nodes, edges, 1);
    expect(result.nodes.map((n) => n.id).sort()).toEqual(["a", "b"]);
    expect(result.capped?.hiddenNodes).toBe(1);
  });

  it("does not reorder the caller's array", () => {
    // The details panel reads the same edge list; sorting in place would reorder it underneath.
    const nodes = [node("a"), node("b"), node("c")];
    const edges = [edge("e1", "a", "b", 1), edge("e2", "a", "c", 999)];
    const before = edges.map((e) => e.id);

    applyRenderBudget(nodes, edges, 1);
    expect(edges.map((e) => e.id)).toEqual(before);
  });

  it("is deterministic when counts tie", () => {
    const nodes = [node("a"), node("b"), node("c")];
    const edges = [edge("zzz", "a", "b", 10), edge("aaa", "a", "c", 10)];

    const first = applyRenderBudget(nodes, [...edges], 1).edges[0]!.id;
    const second = applyRenderBudget(nodes, [...edges], 1).edges[0]!.id;
    expect(first).toBe(second);
    expect(first).toBe("aaa");
  });

  it("caps below the size measured to hang the browser", () => {
    // 172 nodes / 1,002 edges never painted within 250 s. The limit must sit well under that.
    expect(MAX_RENDERED_EDGES).toBeLessThan(1000);
    // And above the largest graph measured to render comfortably (307 edges).
    expect(MAX_RENDERED_EDGES).toBeGreaterThanOrEqual(307);
  });
});
