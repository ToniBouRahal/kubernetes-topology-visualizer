import type { NodeKind } from "../../api/types";

/**
 * Visual encoding rules.
 *
 * Every node is drawn with ONE shape. Kind was carried by shape until the shape vocabulary was
 * removed deliberately: seven outlines had to be learned from a legend before the picture could
 * be read, and the kind is already written on every node in words (see TopologyNode), which is
 * the cue a screen reader and a greyscale print both get. ADR-006 D-6.3 asks that colour never be
 * the only carrier of a fact — the written kind satisfies that, so colour stays free for
 * NAMESPACE.
 *
 * The external node keeps a dashed outline. That is a stroke, not a shape: it marks a boundary —
 * everything outside the cluster collapses into one node and no remote address is ever stored —
 * and that claim is worth a visual mark of its own.
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

/** One shape, drawn for every node. Kept as a named type so the call sites stay explicit. */
export type ShapeName = "rounded";

interface KindEncoding {
  shape: ShapeName;
  /** Spoken/echoed on the node and in the accessible name — never colour alone. */
  label: string;
  /** Dashed outline. Only the external node: it marks the cluster boundary, not a kind. */
  dashed?: boolean;
}

export const KIND_ENCODING: Record<string, KindEncoding> = {
  Service: { shape: "rounded", label: "Service" },
  Deployment: { shape: "rounded", label: "Deployment" },
  StatefulSet: { shape: "rounded", label: "StatefulSet" },
  DaemonSet: { shape: "rounded", label: "DaemonSet" },
  Job: { shape: "rounded", label: "Job" },
  Pod: { shape: "rounded", label: "Pod" },
  External: { shape: "rounded", label: "External", dashed: true },
};

export function encodingFor(kind: NodeKind | string): KindEncoding {
  return KIND_ENCODING[kind] ?? KIND_ENCODING.Pod!;
}

/**
 * SVG path for a node's outline, drawn to fill w x h.
 *
 * A path rather than clip-path or a CSS border radius, so the outline is a real stroke: it has to
 * stay legible at low zoom, when the fill is too small to read.
 */
export function shapePath(_shape: ShapeName, w: number, h: number): string {
  const r = 7;
  return `M ${r} 0 H ${w - r} A ${r} ${r} 0 0 1 ${w} ${r} V ${h - r} A ${r} ${r} 0 0 1 ${w - r} ${h} H ${r} A ${r} ${r} 0 0 1 0 ${h - r} V ${r} A ${r} ${r} 0 0 1 ${r} 0 Z`;
}
