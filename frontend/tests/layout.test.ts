import { describe, expect, it } from "vitest";

import type { GraphEdge, GraphNode } from "../src/api/types";
import { encodingFor, namespaceHue } from "../src/features/graph/encoding";
import { edgeWidth, layoutGraph } from "../src/features/graph/layout";

function node(id: string, name: string, kind = "Deployment", namespace = "demo"): GraphNode {
  return {
    id,
    kind: kind as GraphNode["kind"],
    namespace,
    name,
    label: name,
    first_seen: "2026-08-17T12:00:00Z",
    last_seen: "2026-08-17T12:05:00Z",
    attributes: {},
  };
}

function edge(id: string, source: string, target: string, count = 10): GraphEdge {
  return {
    id,
    source_id: source,
    target_id: target,
    protocol: "TCP",
    destination_port: 8080,
    connection_count: count,
    bytes_sent: null,
    bytes_received: null,
    first_seen: "2026-08-17T12:00:00Z",
    last_seen: "2026-08-17T12:05:00Z",
  };
}

describe("layout stability (T-6.2)", () => {
  const nodes = [node("a", "alpha"), node("b", "beta"), node("c", "gamma")];
  const edges = [edge("e1", "a", "b"), edge("e2", "b", "c")];

  it("produces identical positions for identical input", () => {
    const first = layoutGraph(nodes, edges);
    const second = layoutGraph(nodes, edges);
    for (const [id, pos] of first.positions) {
      expect(second.positions.get(id)).toEqual(pos);
    }
  });

  it("does not recompute when only counts change", () => {
    // The realistic polling case: same topology, higher numbers. Nothing may move.
    const first = layoutGraph(nodes, edges);
    const busier = edges.map((e) => ({ ...e, connection_count: e.connection_count * 7 }));
    const second = layoutGraph(nodes, busier, first);

    expect(second.recomputed).toBe(false);
    expect(second.positions).toBe(first.positions);
  });

  it("is insensitive to the order nodes arrive in", () => {
    const first = layoutGraph(nodes, edges);
    const shuffled = layoutGraph([...nodes].reverse(), [...edges].reverse());
    for (const [id, pos] of first.positions) {
      expect(shuffled.positions.get(id)).toEqual(pos);
    }
  });

  it("keeps existing nodes in place when a new one appears", () => {
    // One new dependency must not rearrange the graph mid-demonstration.
    const first = layoutGraph(nodes, edges);
    const grown = layoutGraph(
      [...nodes, node("d", "delta")],
      [...edges, edge("e3", "c", "d")],
      first,
    );

    expect(grown.recomputed).toBe(true);
    for (const id of ["a", "b", "c"]) {
      expect(grown.positions.get(id)).toEqual(first.positions.get(id));
    }
    expect(grown.positions.has("d")).toBe(true);
  });

  it("lays out nodes without overlapping them", () => {
    const { positions } = layoutGraph(nodes, edges);
    const seen = [...positions.values()].map((p) => `${p.x},${p.y}`);
    expect(new Set(seen).size).toBe(seen.length);
  });

  it("ignores an edge whose endpoint is not in the node set", () => {
    // Truncation can drop a node while an edge still references it.
    expect(() => layoutGraph(nodes, [...edges, edge("e9", "a", "missing")])).not.toThrow();
  });
});

describe("edge width (D-6.4)", () => {
  it("stays within bounds and rises with volume", () => {
    expect(edgeWidth(0, 100)).toBeLessThanOrEqual(2);
    expect(edgeWidth(100, 100)).toBeLessThanOrEqual(6);
    expect(edgeWidth(90, 100)).toBeGreaterThan(edgeWidth(5, 100));
  });

  it("is logarithmic, so a single huge edge does not flatten the rest", () => {
    // Linear scaling would render everything below the busiest edge as a hairline.
    const small = edgeWidth(10, 10_000);
    const huge = edgeWidth(10_000, 10_000);
    expect(small).toBeGreaterThan(1.5);
    expect(huge / small).toBeLessThan(4);
  });

  it("handles an empty graph without dividing by zero", () => {
    expect(Number.isFinite(edgeWidth(0, 0))).toBe(true);
  });
});

describe("visual encoding (D-6.3)", () => {
  it("gives every allowed kind its own shape", () => {
    const kinds = ["Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "Pod", "External"];
    const shapes = kinds.map((k) => encodingFor(k).shape);
    expect(new Set(shapes).size).toBe(kinds.length);
  });

  it("names every kind in words, so shape is never the only cue", () => {
    for (const kind of ["Service", "Deployment", "Pod", "External"]) {
      expect(encodingFor(kind).label.length).toBeGreaterThan(0);
    }
  });

  it("assigns a namespace the same colour every time", () => {
    // An index-based assignment would re-colour everything when a namespace appeared.
    expect(namespaceHue("demo")).toBe(namespaceHue("demo"));
    expect(namespaceHue("data")).toBe(namespaceHue("data"));
  });

  it("gives the external node its own treatment", () => {
    expect(namespaceHue(null)).toBe("var(--external)");
    expect(encodingFor("External").shape).toBe("dashed-pill");
  });
});
