/**
 * Comparison node resolution — ADR-006 D-6.3, and the ids-are-opaque invariant in ADR-003.
 *
 * A comparison references nodes the live graph has never shown: anything that existed only in the
 * baseline period is precisely what REMOVED means. `useDiff` therefore fetches the graph for BOTH
 * periods so the real node records are available. The earlier implementation instead derived
 * kind/namespace/name by splitting the id, which `contracts/ids.md` §2 forbids.
 */
import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { DiffQuery, GraphNode } from "../src/api/types";
import { useDiff } from "../src/features/graph/useDiff";

const QUERY: DiffQuery = {
  baselineFrom: "2026-08-21T10:00:00Z",
  baselineTo: "2026-08-21T10:05:00Z",
  currentFrom: "2026-08-21T10:05:00Z",
  currentTo: "2026-08-21T10:10:00Z",
};

function node(id: string, name: string, namespace: string | null): GraphNode {
  return {
    id,
    kind: "Deployment",
    namespace,
    name,
    label: name,
    first_seen: "2026-08-21T10:00:00Z",
    last_seen: "2026-08-21T10:10:00Z",
    attributes: {},
  } as GraphNode;
}

const BASELINE_ONLY = "k8s:c1:demo:Deployment:retired";
const IN_BOTH = "k8s:c1:demo:Deployment:backend";

function diffBody() {
  return {
    generated_at: "2026-08-21T10:10:00Z",
    baseline: { start: QUERY.baselineFrom, end: QUERY.baselineTo },
    current: { start: QUERY.currentFrom, end: QUERY.currentTo },
    threshold_percent: 20,
    filters: {
      namespaces: [],
      kind: null,
      query: null,
      include_external: true,
      include_unresolved: false,
    },
    include_unchanged: false,
    edges: [
      {
        id: "e1",
        source_id: BASELINE_ONLY,
        target_id: IN_BOTH,
        protocol: "TCP",
        destination_port: 8080,
        classification: "REMOVED",
        baseline_connection_count: 10,
        current_connection_count: 0,
        connection_percent_delta: -100,
        baseline_bytes_total: null,
        current_bytes_total: null,
        bytes_percent_delta: null,
      },
    ],
    summary: { new: 0, removed: 1, changed: 0, unchanged: 0, total: 1 },
  };
}

function graphBody(nodes: GraphNode[], window: { start: string; end: string }) {
  return {
    generated_at: "2026-08-21T10:10:00Z",
    window,
    filters: {
      namespaces: [],
      kind: null,
      query: null,
      include_external: true,
      include_unresolved: false,
    },
    nodes,
    edges: [],
    summary: {
      node_count: nodes.length,
      edge_count: 0,
      total_connections: 0,
      truncated: false,
      truncation_reason: null,
    },
  };
}

const ok = (body: unknown) =>
  Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("useDiff", () => {
  it("fetches both periods' graphs and exposes their nodes", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string | URL) => {
        const u = String(url);
        urls.push(u);
        if (u.includes("/diff")) return ok(diffBody());
        // The baseline period still has the node that later disappears.
        if (u.includes(encodeURIComponent(QUERY.baselineFrom)) || u.includes(QUERY.baselineFrom)) {
          return ok(
            graphBody(
              [node(BASELINE_ONLY, "retired", "demo"), node(IN_BOTH, "backend", "demo")],
              { start: QUERY.baselineFrom, end: QUERY.baselineTo },
            ),
          );
        }
        return ok(
          graphBody([node(IN_BOTH, "backend", "demo")], {
            start: QUERY.currentFrom,
            end: QUERY.currentTo,
          }),
        );
      }),
    );

    const { result } = renderHook(() => useDiff(QUERY));
    await waitFor(() => expect(result.current.diff).not.toBeNull());
    await waitFor(() => expect(result.current.nodes.size).toBeGreaterThan(0));

    expect(urls.filter((u) => u.includes("/graph"))).toHaveLength(2);

    // The whole point: a node that exists ONLY in the baseline is still fully described, with a
    // real name and namespace rather than fragments of its id.
    const retired = result.current.nodes.get(BASELINE_ONLY);
    expect(retired).toBeDefined();
    expect(retired?.name).toBe("retired");
    expect(retired?.namespace).toBe("demo");
    expect(retired?.kind).toBe("Deployment");

    expect(result.current.nodes.get(IN_BOTH)?.name).toBe("backend");
  });

  it("clears diff and nodes when the query goes null", async () => {
    vi.stubGlobal("fetch", vi.fn(() => ok(diffBody())));
    const { result, rerender } = renderHook(({ q }: { q: DiffQuery | null }) => useDiff(q), {
      initialProps: { q: null as DiffQuery | null },
    });
    expect(result.current.diff).toBeNull();
    expect(result.current.nodes.size).toBe(0);
    rerender({ q: null });
    expect(result.current.nodes.size).toBe(0);
  });

  it("surfaces an error without leaving a stale comparison on screen", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ detail: "comparison failed" }), { status: 500 }),
        ),
      ),
    );
    const { result } = renderHook(() => useDiff(QUERY));
    await waitFor(() => expect(result.current.error).not.toBeNull());
    expect(result.current.diff).toBeNull();
  });
});
