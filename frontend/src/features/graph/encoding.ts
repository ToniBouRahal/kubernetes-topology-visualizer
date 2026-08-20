import type { NodeKind } from "../../api/types";

/**
 * Visual encoding rules.
 *
 * KIND is carried by SHAPE, colour only reinforces (ADR-006 D-6.3). That is not merely an
 * accessibility box to tick: it frees colour to encode NAMESPACE, which shape cannot express,
 * so the two channels carry two facts instead of one fact twice.
 */

export const NAMESPACE_HUES = [
  "var(--ns-1)",
  "var(--ns-2)",
  "var(--ns-3)",
  "var(--ns-4)",
  "var(--ns-5)",
  "var(--ns-6)",
] as const;

/**
 * Assign a namespace its colour deterministically from its name.
 *
 * Deterministic, not index-based: an index would reassign every colour the moment a new
 * namespace appeared or a filter changed, so `demo` would silently change hue between polls.
 */
export function namespaceHue(namespace: string | null | undefined): string {
  if (!namespace) return "var(--external)";
  let hash = 0;
  for (let i = 0; i < namespace.length; i += 1) {
    hash = (hash * 31 + namespace.charCodeAt(i)) >>> 0;
  }
  return NAMESPACE_HUES[hash % NAMESPACE_HUES.length]!;
}

export type ShapeName =
  | "hexagon"
  | "rounded"
  | "spine"
  | "notched"
  | "clipped"
  | "pill"
  | "dashed-pill";

interface KindEncoding {
  shape: ShapeName;
  /** Spoken/echoed in the legend and in the accessible name — never colour alone. */
  label: string;
  /** Why this shape, so the legend can explain itself rather than just asserting. */
  rationale: string;
}

export const KIND_ENCODING: Record<string, KindEncoding> = {
  Service: {
    shape: "hexagon",
    label: "Service",
    rationale: "a routing point traffic passes through",
  },
  Deployment: {
    shape: "rounded",
    label: "Deployment",
    rationale: "interchangeable replicas",
  },
  StatefulSet: {
    shape: "spine",
    label: "StatefulSet",
    rationale: "the bar marks ordered, stable identity",
  },
  DaemonSet: {
    shape: "notched",
    label: "DaemonSet",
    rationale: "notches mark one instance per node",
  },
  Job: {
    shape: "clipped",
    label: "Job",
    rationale: "the cut corner marks work that finishes",
  },
  Pod: {
    shape: "pill",
    label: "Pod",
    rationale: "a single unit with no controller",
  },
  External: {
    shape: "dashed-pill",
    label: "External",
    rationale: "outside the cluster; the dashed edge marks an unmeasured interior",
  },
};

export function encodingFor(kind: NodeKind | string): KindEncoding {
  return KIND_ENCODING[kind] ?? KIND_ENCODING.Pod!;
}

/**
 * SVG path for a node's shape, drawn to fill w x h.
 *
 * Paths rather than clip-path so the outline is a real stroke: the shape has to stay legible as
 * an outline at low zoom, when the fill is too small to read.
 */
export function shapePath(shape: ShapeName, w: number, h: number): string {
  const r = 7;
  const notch = 9;

  switch (shape) {
    case "hexagon": {
      const inset = 13;
      return `M ${inset} 0 H ${w - inset} L ${w} ${h / 2} L ${w - inset} ${h} H ${inset} L 0 ${h / 2} Z`;
    }
    case "spine":
      // Square left edge reads as the fixed end of an ordered set; rounded right end does not.
      return `M 0 0 H ${w - r} A ${r} ${r} 0 0 1 ${w} ${r} V ${h - r} A ${r} ${r} 0 0 1 ${w - r} ${h} H 0 Z`;
    case "notched":
      return [
        `M ${notch} 0 H ${w - notch}`,
        `L ${w} ${notch} V ${h - notch}`,
        `L ${w - notch} ${h} H ${notch}`,
        `L 0 ${h - notch} V ${notch} Z`,
      ].join(" ");
    case "clipped": {
      const cut = 14;
      return `M 0 ${r} A ${r} ${r} 0 0 1 ${r} 0 H ${w - cut} L ${w} ${cut} V ${h - r} A ${r} ${r} 0 0 1 ${w - r} ${h} H ${r} A ${r} ${r} 0 0 1 0 ${h - r} Z`;
    }
    case "pill":
    case "dashed-pill": {
      const rad = h / 2;
      return `M ${rad} 0 H ${w - rad} A ${rad} ${rad} 0 0 1 ${w - rad} ${h} H ${rad} A ${rad} ${rad} 0 0 1 ${rad} 0 Z`;
    }
    case "rounded":
    default:
      return `M ${r} 0 H ${w - r} A ${r} ${r} 0 0 1 ${w} ${r} V ${h - r} A ${r} ${r} 0 0 1 ${w - r} ${h} H ${r} A ${r} ${r} 0 0 1 0 ${h - r} V ${r} A ${r} ${r} 0 0 1 ${r} 0 Z`;
  }
}

/** The StatefulSet spine, drawn separately so it can sit inside the outline. */
export function spineMark(h: number): string {
  return `M 3.5 4 V ${h - 4}`;
}
