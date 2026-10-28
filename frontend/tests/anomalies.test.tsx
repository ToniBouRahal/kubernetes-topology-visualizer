/** Anomaly list and canary marking — ADR-016 T-16.8. */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AnomalyResponse, GraphNode } from "../src/api/types";
import { CanaryBanner } from "../src/components/States";
import { AnomalyList, anomalyEnds } from "../src/features/anomalies/AnomalyList";
import { TopologyNode } from "../src/features/graph/TopologyNode";

// React Flow's Handle needs a provider; the node's own markup is what is under test here.
vi.mock("@xyflow/react", () => ({ Handle: () => null, Position: {} }));

afterEach(cleanup);

const node = (id: string, label: string, canary = false): GraphNode => ({
  id,
  kind: "Deployment",
  namespace: "demo",
  name: label,
  label,
  first_seen: "2026-08-10T11:00:00Z",
  last_seen: "2026-08-10T11:59:00Z",
  attributes: {},
  canary,
});

function response(overrides: Partial<AnomalyResponse> = {}): AnomalyResponse {
  return {
    generated_at: "2026-08-10T12:00:00Z",
    window: { start: "2026-08-10T11:55:00Z", end: "2026-08-10T12:00:00Z" },
    baseline: { start: "2026-08-09T11:55:00Z", end: "2026-08-10T11:55:00Z" },
    filters: { namespaces: [], include_external: true, include_unresolved: false },
    nodes: [node("src-id", "reporter"), node("dst-id", "legacy-admin", true)],
    anomalies: [
      {
        id: "e|CANARY_CONTACT",
        edge_id: "e",
        type: "CANARY_CONTACT",
        severity: "critical",
        source_id: "src-id",
        target_id: "dst-id",
        protocol: "TCP",
        destination_port: 22,
        current_connection_count: 3,
        baseline_connection_count: 0,
        reason: "3 connection attempt(s) to a canary workload; nothing legitimate connects to one",
      },
    ],
    summary: { critical_count: 1, warning_count: 0, baseline_observed: true, truncated: false },
    ...overrides,
  };
}

describe("AnomalyList", () => {
  it("names severity and rule in words, with labels from the response and the reason", () => {
    render(<AnomalyList response={response()} onSelect={() => {}} />);
    expect(screen.getByText("critical")).toBeInTheDocument();
    expect(screen.getByText("Canary contacted")).toBeInTheDocument();
    expect(screen.getByText("reporter → legacy-admin :22")).toBeInTheDocument();
    expect(screen.getByText(/nothing legitimate connects/)).toBeInTheDocument();
  });

  it("selects the source workload when an anomaly is chosen", () => {
    const select = vi.fn();
    render(<AnomalyList response={response()} onSelect={select} />);
    fireEvent.click(screen.getByRole("button", { name: /Canary contacted/ }));
    expect(select).toHaveBeenCalledWith("src-id");
  });

  it("says when nothing was found, against which baseline", () => {
    render(<AnomalyList response={response({ anomalies: [] })} onSelect={() => {}} />);
    expect(screen.getByText("No anomalies against the previous 24 h.")).toBeInTheDocument();
  });

  it("states plainly that novelty rules are off without a baseline — D-16.2", () => {
    const r = response({
      anomalies: [],
      summary: { critical_count: 0, warning_count: 0, baseline_observed: false, truncated: false },
    });
    render(<AnomalyList response={r} onSelect={() => {}} />);
    expect(screen.getByText(/No baseline yet/)).toBeInTheDocument();
  });

  it("shows an id verbatim, never taken apart, when its node record is missing", () => {
    const r = response({ nodes: [] });
    expect(anomalyEnds(r, r.anomalies[0]!)).toEqual(["src-id", "dst-id"]);
  });
});

describe("canary marking — D-16.4", () => {
  it("marks a canary node with its own class and words, not a hue", () => {
    const { container } = render(<TopologyNode data={{ node: node("dst-id", "legacy-admin", true) }} />);
    const root = container.querySelector(".topology-node")!;
    expect(root.className).toContain("topology-node--canary");
    expect(root.getAttribute("aria-label")).toContain("canary");
    expect(screen.getByText("canary · demo")).toBeInTheDocument();
  });

  it("leaves an ordinary node unmarked", () => {
    const { container } = render(<TopologyNode data={{ node: node("src-id", "reporter") }} />);
    expect(container.querySelector(".topology-node--canary")).toBeNull();
    expect(screen.getByText("demo")).toBeInTheDocument();
  });

  it("announces canary contact above the graph", () => {
    render(<CanaryBanner contacts={[["reporter", "legacy-admin"]]} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Canary contacted: reporter → legacy-admin");
  });
});

describe("useAnomalies", () => {
  it("ignores a body that is not an anomaly response, leaving the page intact", async () => {
    const { renderHook, waitFor } = await import("@testing-library/react");
    const { useAnomalies } = await import("../src/features/anomalies/useAnomalies");
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ nodes: [], edges: [] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useAnomalies({ window: "5m" }, null));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(result.current).toBeNull();
    vi.unstubAllGlobals();
  });

  it("is off, and asks for nothing, in compare mode", async () => {
    const { renderHook } = await import("@testing-library/react");
    const { useAnomalies } = await import("../src/features/anomalies/useAnomalies");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useAnomalies(null, null));
    expect(result.current).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
