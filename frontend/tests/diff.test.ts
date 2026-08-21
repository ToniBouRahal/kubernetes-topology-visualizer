import { describe, expect, it } from "vitest";

import type { DiffEdge } from "../src/api/types";
import { diffStyle } from "../src/features/graph/diffEncoding";
import { adjacentPeriods } from "../src/features/timerange/periods";

function diffEdge(classification: string, overrides: Partial<DiffEdge> = {}): DiffEdge {
  return {
    id: "a|b|TCP|8080",
    source_id: "k8s:c:demo:Deployment:a",
    target_id: "k8s:c:demo:Service:b",
    protocol: "TCP",
    destination_port: 8080,
    classification: classification as DiffEdge["classification"],
    baseline_connection_count: 100,
    current_connection_count: 150,
    connection_delta: 50,
    connection_percent_delta: 50,
    baseline_bytes_total: null,
    current_bytes_total: null,
    bytes_percent_delta: null,
    reason: "",
    ...overrides,
  };
}

describe("diff encoding (D-6.3)", () => {
  it("gives every classification a distinct stroke pattern", () => {
    const patterns = ["NEW", "REMOVED", "CHANGED", "UNCHANGED"].map(
      (c) => `${diffStyle(diffEdge(c)).dash ?? "solid"}-${diffStyle(diffEdge(c)).width}`,
    );
    expect(new Set(patterns).size).toBe(4);
  });

  it("names every classification in words, so colour is never the only cue", () => {
    for (const c of ["NEW", "REMOVED", "CHANGED", "UNCHANGED"]) {
      expect(diffStyle(diffEdge(c)).badge).toContain(c);
    }
  });

  it("remains distinguishable with colour removed", () => {
    // The property that matters for a colour-blind reader: strip colour, and the four states
    // must still differ by pattern+width alone.
    const withoutColour = ["NEW", "REMOVED", "CHANGED", "UNCHANGED"].map((c) => {
      const s = diffStyle(diffEdge(c));
      return JSON.stringify({ dash: s.dash ?? null, width: s.width, badge: s.badge });
    });
    expect(new Set(withoutColour).size).toBe(4);
  });

  it("puts the magnitude in the CHANGED badge", () => {
    expect(diffStyle(diffEdge("CHANGED")).badge).toBe("CHANGED +50%");
    expect(
      diffStyle(diffEdge("CHANGED", { connection_percent_delta: -30 })).badge,
    ).toBe("CHANGED -30%");
  });

  it("prefers byte percentage when it was measured", () => {
    const badge = diffStyle(
      diffEdge("CHANGED", { bytes_percent_delta: 120, connection_percent_delta: 5 }),
    ).badge;
    expect(badge).toBe("CHANGED +120%");
  });

  it("falls back to a bare CHANGED when no percentage is defined", () => {
    expect(
      diffStyle(diffEdge("CHANGED", { connection_percent_delta: null })).badge,
    ).toBe("CHANGED");
  });
});

describe("comparison periods", () => {
  const now = new Date("2026-08-21T12:00:00Z");

  it("produces two adjacent windows of equal length", () => {
    const p = adjacentPeriods(5, now);
    expect(p.baselineTo).toBe(p.currentFrom);
    expect(new Date(p.currentTo).getTime() - new Date(p.currentFrom).getTime()).toBe(5 * 60_000);
    expect(new Date(p.baselineTo).getTime() - new Date(p.baselineFrom).getTime()).toBe(5 * 60_000);
  });

  it("does not overlap", () => {
    // The backend rejects overlap, because a shared interval would be counted on both sides.
    const p = adjacentPeriods(15, now);
    expect(new Date(p.baselineTo).getTime()).toBeLessThanOrEqual(
      new Date(p.currentFrom).getTime(),
    );
  });

  it("ends at the supplied instant", () => {
    expect(adjacentPeriods(60, now).currentTo).toBe(now.toISOString());
  });
});
