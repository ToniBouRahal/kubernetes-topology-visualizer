import { describe, expect, it } from "vitest";

import { BEND_STEP, edgeBends, floatingPath, routeBends } from "../src/features/graph/edgeGeometry";

/** The endpoints of an "M x y L x y" or "M x y Q cx cy x y" path. */
function ends(path: string) {
  const n = path.match(/-?\d+(\.\d+)?/g)!.map(Number);
  return { start: { x: n[0]!, y: n[1]! }, end: { x: n[n.length - 2]!, y: n[n.length - 1]! } };
}

describe("floatingPath", () => {
  it("leaves a source on the side facing a target BEHIND it — the demo's broken arrow", () => {
    // frontend moved below-left of backend by the layout; a fixed right→left handle pair looped.
    const frontend = { x: 225, y: 318, r: 34 };
    const backend = { x: 243, y: 207, r: 40 };
    const { start, end } = ends(floatingPath(frontend, backend, 0)!.path);

    // Leaves frontend's TOP, heading up to backend, and enters backend's BOTTOM.
    expect(start.y).toBeLessThan(frontend.y);
    expect(end.y).toBeGreaterThan(backend.y);
    // Both ends sit on their circles, not inside or past them.
    expect(Math.hypot(start.x - frontend.x, start.y - frontend.y)).toBeCloseTo(frontend.r);
    expect(Math.hypot(end.x - backend.x, end.y - backend.y)).toBeCloseTo(backend.r);
  });

  it("points the right way whichever side the target is on", () => {
    const a = { x: 0, y: 0, r: 40 };
    for (const [tx, ty] of [[300, 0], [-300, 0], [0, 300], [0, -300], [200, -200]] as const) {
      const b = { x: tx, y: ty, r: 40 };
      const { start, end } = ends(floatingPath(a, b, 0)!.path);
      // The edge runs from a towards b: its direction agrees with a→b.
      expect((end.x - start.x) * tx + (end.y - start.y) * ty).toBeGreaterThan(0);
    }
  });

  it("puts the label on the curve, bowed to the left of travel", () => {
    const a = { x: 0, y: 0, r: 40 };
    const b = { x: 400, y: 0, r: 40 };
    const straight = floatingPath(a, b, 0)!;
    expect(straight.labelY).toBeCloseTo(0);
    // Travelling +x with y pointing down, left of travel is -y.
    const bent = floatingPath(a, b, 40)!;
    expect(bent.labelY).toBeLessThan(0);
    expect(floatingPath(b, a, 40)!.labelY).toBeGreaterThan(0);
  });

  it("draws nothing between circles that touch", () => {
    expect(floatingPath({ x: 0, y: 0, r: 40 }, { x: 70, y: 0, r: 40 }, 0)).toBeNull();
  });
});

describe("edgeBends", () => {
  const e = (id: string, s: string, t: string) => ({ id, source_id: s, target_id: t });

  it("keeps a lone edge straight", () => {
    expect(edgeBends([e("1", "a", "b"), e("2", "b", "c")])).toEqual(new Map([["1", 0], ["2", 0]]));
  });

  it("gives every edge between the same two nodes its own curve", () => {
    // Two ports a→b, and b→a back.
    const bends = edgeBends([e("x", "a", "b"), e("y", "a", "b"), e("z", "b", "a")]);
    expect(new Set(bends.values()).size).toBe(3);
    expect([...bends.values()].every((b) => b >= BEND_STEP)).toBe(true);
  });
});

describe("routeBends", () => {
  const e = (id: string, s: string, t: string) => ({ id, source_id: s, target_id: t });
  /** Closest distance from a disc's centre to the drawn curve, sampled. */
  function clearance(a: { x: number; y: number; r: number }, b: { x: number; y: number; r: number }, bend: number, o: { x: number; y: number }) {
    const n = floatingPath(a, b, bend)!.path.match(/-?\d+(\.\d+)?/g)!.map(Number);
    const [sx, sy] = [n[0]!, n[1]!];
    const [ex, ey] = [n[n.length - 2]!, n[n.length - 1]!];
    const [cx, cy] = n.length === 6 ? [n[2]!, n[3]!] : [(sx + ex) / 2, (sy + ey) / 2];
    let best = Infinity;
    for (let i = 0; i <= 200; i++) {
      const t = i / 200;
      const x = (1 - t) ** 2 * sx + 2 * t * (1 - t) * cx + t ** 2 * ex;
      const y = (1 - t) ** 2 * sy + 2 * t * (1 - t) * cy + t ** 2 * ey;
      best = Math.min(best, Math.hypot(x - o.x, y - o.y));
    }
    return best;
  }

  it("curves an edge around a node sitting on its straight line — demo-traffic → redis through backend", () => {
    const discs = new Map([
      ["demo-traffic", { x: 0, y: 0, r: 26 }],
      ["backend", { x: 135, y: -8, r: 30 }],
      ["redis", { x: 270, y: 0, r: 26 }],
    ]);
    const bends = routeBends([e("x", "demo-traffic", "redis")], discs, new Map());
    const bend = bends.get("x")!;
    expect(bend).not.toBe(0);
    expect(clearance(discs.get("demo-traffic")!, discs.get("redis")!, bend, discs.get("backend")!)).toBeGreaterThan(30);
  });

  it("leaves an unobstructed edge exactly as the parallel bend had it", () => {
    const discs = new Map([["a", { x: 0, y: 0, r: 40 }], ["b", { x: 300, y: 0, r: 40 }], ["c", { x: 150, y: 200, r: 40 }]]);
    expect(routeBends([e("x", "a", "b")], discs, new Map([["x", 22]])).get("x")).toBe(22);
  });

  it("picks the side that needs less curve", () => {
    // The obstacle sits slightly LEFT of travel (-y), so passing on the right is shorter.
    const discs = new Map([["a", { x: 0, y: 0, r: 40 }], ["b", { x: 400, y: 0, r: 40 }], ["o", { x: 200, y: -10, r: 40 }]]);
    expect(routeBends([e("x", "a", "b")], discs, new Map()).get("x")!).toBeLessThan(0);
  });
});
