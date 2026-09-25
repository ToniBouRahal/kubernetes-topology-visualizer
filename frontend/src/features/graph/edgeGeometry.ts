/** Pixels between successive curves drawn between the same two nodes. */
export const BEND_STEP = 22;

/**
 * How far each edge curves (FloatingEdge `data.bend`), so edges between the same two nodes stay
 * apart.
 *
 * A lone edge is straight. Where two nodes share several edges — two ports, or traffic both ways
 * — each gets its own curve. A curve bows to the left of its direction of travel, so A→B and B→A
 * separate on their own; same-direction edges step outwards one BEND_STEP at a time.
 */
export function edgeBends(edges: { id: string; source_id: string; target_id: string }[]): Map<string, number> {
  const byPair = new Map<string, string[]>();
  for (const e of edges) {
    const key = [e.source_id, e.target_id].sort().join("\u0000");
    byPair.set(key, [...(byPair.get(key) ?? []), e.id]);
  }
  const bends = new Map<string, number>();
  for (const ids of byPair.values()) {
    ids.sort();
    ids.forEach((id, i) => bends.set(id, ids.length === 1 ? 0 : BEND_STEP * (i + 1)));
  }
  return bends;
}

/** Space kept between an edge and a node it is routed around. */
export const EDGE_CLEARANCE = 14;
/** The strongest curve a detour may take, as a share of the edge's length. */
const MAX_BEND_RATIO = 0.9;

/**
 * Bends that route each edge around the nodes a straight line would cross, on top of the
 * parallel-edge bends from `edgeBends`.
 *
 * A straight edge between two circles is exactly right until a third circle sits on the line —
 * three nodes in one rank do, and the layout lines them up often. For each edge, every other disc
 * within reach of the straight line is an obstacle; the edge bows to whichever side needs the
 * smaller curve to clear them all, and its parallel bend is stacked on the same side so siblings
 * stay apart.
 *
 * A quadratic curve with bend h is displaced 2t(1-t)·h at parameter t, which is what the
 * clearance at each obstacle is solved against. Positions are the layout's, so this runs when the
 * layout changes, not per frame.
 */
export function routeBends(
  edges: { id: string; source_id: string; target_id: string }[],
  discs: Map<string, Disc>,
  parallel: Map<string, number>,
): Map<string, number> {
  const all = [...discs.entries()];
  const bends = new Map<string, number>();
  for (const e of edges) {
    const base = parallel.get(e.id) ?? 0;
    const a = discs.get(e.source_id);
    const b = discs.get(e.target_id);
    if (!a || !b || e.source_id === e.target_id) {
      bends.set(e.id, base);
      continue;
    }
    const dx = b.x - a.x;
    const dy = b.y - a.y;
    const length = Math.hypot(dx, dy);
    if (length === 0) {
      bends.set(e.id, base);
      continue;
    }
    // Left of travel, matching floatingPath's convention.
    const lx = dy / length;
    const ly = -dx / length;
    const mx = (a.x + b.x) / 2;
    const my = (a.y + b.y) / 2;

    // The curve needed on each side to clear every obstacle.
    let needLeft = 0;
    let needRight = 0;
    let blocked = false;
    for (const [id, o] of all) {
      if (id === e.source_id || id === e.target_id) continue;
      const t = ((o.x - a.x) * dx + (o.y - a.y) * dy) / (length * length);
      if (t <= 0 || t >= 1) continue;
      const d = (o.x - mx) * lx + (o.y - my) * ly;
      const reach = o.r + EDGE_CLEARANCE;
      if (Math.abs(d) >= reach) continue;
      blocked = true;
      const f = 2 * t * (1 - t);
      // Passing on the side the obstacle is NOT on needs reach - |d|; passing on its own side
      // means going all the way round it, |d| + reach.
      const away = (reach - Math.abs(d)) / f;
      const round = (Math.abs(d) + reach) / f;
      if (d >= 0) {
        needRight = Math.max(needRight, away);
        needLeft = Math.max(needLeft, round);
      } else {
        needLeft = Math.max(needLeft, away);
        needRight = Math.max(needRight, round);
      }
    }
    if (!blocked) {
      bends.set(e.id, base);
      continue;
    }
    const cap = MAX_BEND_RATIO * length;
    const bend = needLeft <= needRight ? Math.min(needLeft, cap) + base : -(Math.min(needRight, cap) + base);
    bends.set(e.id, bend);
  }
  return bends;
}

export interface Disc {
  x: number;
  y: number;
  r: number;
}

/**
 * The path of an edge from circle `a` to circle `b`, leaving and entering each on the side that
 * faces the other, and the point on it where its label goes. Null when the circles touch.
 *
 * `bend` curves it to the left of its direction of travel, in pixels at the midpoint (y points
 * down, as on screen).
 */
export function floatingPath(a: Disc, b: Disc, bend: number): { path: string; labelX: number; labelY: number } | null {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const length = Math.hypot(dx, dy);
  if (length <= a.r + b.r) return null;

  const ux = dx / length;
  const uy = dy / length;
  const cx = (a.x + b.x) / 2 + uy * bend;
  const cy = (a.y + b.y) / 2 - ux * bend;

  // Aimed at the control point, so a curved edge meets each circle square-on.
  const start = towards(a, cx, cy);
  const end = towards(b, cx, cy);
  const path = bend === 0
    ? `M ${start.x} ${start.y} L ${end.x} ${end.y}`
    : `M ${start.x} ${start.y} Q ${cx} ${cy} ${end.x} ${end.y}`;
  // The quadratic's own midpoint (t = 0.5), so the label sits on the line it names.
  return {
    path,
    labelX: 0.25 * start.x + 0.5 * cx + 0.25 * end.x,
    labelY: 0.25 * start.y + 0.5 * cy + 0.25 * end.y,
  };
}

/** The point on a circle's edge facing (px, py). */
function towards(c: Disc, px: number, py: number) {
  const dx = px - c.x;
  const dy = py - c.y;
  const d = Math.hypot(dx, dy) || 1;
  return { x: c.x + (dx / d) * c.r, y: c.y + (dy / d) * c.r };
}
