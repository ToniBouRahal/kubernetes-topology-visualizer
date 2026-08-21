import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { GraphResponse } from "../src/api/types";
import { useGraph } from "../src/features/graph/useGraph";

function response(edgeCount: number, connections = 10): GraphResponse {
  return {
    generated_at: "2026-08-17T12:00:00Z",
    window: { start: "2026-08-17T11:55:00Z", end: "2026-08-17T12:00:00Z" },
    filters: {
      namespaces: [],
      kind: null,
      query: null,
      include_external: true,
      include_unresolved: false,
    },
    nodes: [],
    edges: Array.from({ length: edgeCount }, (_, i) => ({
      id: `e${i}`,
      source_id: "a",
      target_id: "b",
      protocol: "TCP" as const,
      destination_port: 8080,
      connection_count: connections,
      bytes_sent: null,
      bytes_received: null,
      first_seen: "2026-08-17T11:55:00Z",
      last_seen: "2026-08-17T12:00:00Z",
    })),
    summary: {
      node_count: 2,
      edge_count: edgeCount,
      total_connections: connections * edgeCount,
      truncated: false,
      truncation_reason: null,
    },
  };
}

function mockFetch(impl: (url: string) => Promise<Response>) {
  vi.stubGlobal("fetch", vi.fn((url: string | URL) => impl(String(url))));
}

const ok = (body: unknown) =>
  Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));

beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("useGraph", () => {
  it("loads a graph", async () => {
    mockFetch(() => ok(response(2)));
    const { result } = renderHook(() => useGraph({ window: "5m" }));

    await waitFor(() => expect(result.current.graph).not.toBeNull());
    expect(result.current.graph?.summary.edge_count).toBe(2);
    expect(result.current.error).toBeNull();
    expect(result.current.lastUpdated).not.toBeNull();
  });

  // T-6.6. A transient blip must not blank a live demonstration.
  it("keeps the last successful graph when a refresh fails", async () => {
    let callCount = 0;
    mockFetch(() => {
      callCount += 1;
      return callCount === 1
        ? ok(response(3))
        : Promise.resolve(
            new Response(JSON.stringify({ detail: "storage unavailable", request_id: "abc123" }), {
              status: 503,
            }),
          );
    });

    const { result } = renderHook(() => useGraph({ window: "5m" }));
    await waitFor(() => expect(result.current.graph?.summary.edge_count).toBe(3));

    await act(async () => {
      result.current.refresh();
    });

    await waitFor(() => expect(result.current.error).not.toBeNull());
    expect(result.current.graph?.summary.edge_count).toBe(3);
    // The backend's own explanation and correlation id are surfaced, not swallowed.
    expect(result.current.error).toContain("storage unavailable");
    expect(result.current.error).toContain("abc123");
  });

  it("clears the error once a later request succeeds", async () => {
    let callCount = 0;
    mockFetch(() => {
      callCount += 1;
      return callCount === 1
        ? Promise.resolve(new Response("{}", { status: 500 }))
        : ok(response(1));
    });

    const { result } = renderHook(() => useGraph({ window: "5m" }));
    await waitFor(() => expect(result.current.error).not.toBeNull());

    await act(async () => {
      result.current.refresh();
    });
    await waitFor(() => expect(result.current.error).toBeNull());
    expect(result.current.graph).not.toBeNull();
  });

  // T-6.10. A slow response overtaking a fast one would silently roll the view backwards.
  it("ignores a stale response that arrives after a newer one", async () => {
    let callCount = 0;
    mockFetch(() => {
      callCount += 1;
      if (callCount === 1) {
        // First request resolves LATE and with older data.
        return new Promise<Response>((resolve) =>
          setTimeout(() => resolve(new Response(JSON.stringify(response(1)), { status: 200 })), 60),
        );
      }
      return ok(response(9));
    });

    const { result } = renderHook(() => useGraph({ window: "5m" }));
    await act(async () => {
      result.current.refresh();
    });

    await waitFor(() => expect(result.current.graph?.summary.edge_count).toBe(9));
    await act(async () => {
      await new Promise((r) => setTimeout(r, 120));
    });

    // The late first response must NOT have replaced the newer one.
    expect(result.current.graph?.summary.edge_count).toBe(9);
  });

  it("does not poll while paused", async () => {
    const fetchMock = vi.fn(() => ok(response(1)));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() => useGraph({ window: "5m" }, { paused: true }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    await act(async () => {
      vi.advanceTimersByTime(20_000);
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("polls on an interval while live", async () => {
    const fetchMock = vi.fn(() => ok(response(1)));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() => useGraph({ window: "5m" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    await act(async () => {
      vi.advanceTimersByTime(11_000);
    });
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(1));
  });

  it("sends filters as query parameters, repeating namespace", async () => {
    const fetchMock = vi.fn((_url: string | URL) => ok(response(0)));
    vi.stubGlobal("fetch", fetchMock);

    renderHook(() =>
      useGraph({ window: "15m", namespace: ["demo", "data"], query: "redis", includeExternal: false }),
    );

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [firstCall] = fetchMock.mock.calls;
    expect(firstCall).toBeDefined();
    const url = String(firstCall![0]);
    expect(url).toContain("window=15m");
    // Repeated key, not comma-joined: a namespace containing a comma must not split.
    expect(url).toContain("namespace=demo");
    expect(url).toContain("namespace=data");
    expect(url).toContain("query=redis");
    expect(url).toContain("include_external=false");
  });
});
