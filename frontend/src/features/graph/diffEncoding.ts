import type { DiffClassification, DiffEdge } from "../../api/types";

/**
 * How a diff classification is drawn.
 *
 * Every state is distinguishable with colour REMOVED: the stroke pattern differs, and a text
 * badge names the classification in words. Colour only reinforces (ADR-006 D-6.3).
 *
 * That is not decoration. Roughly one in twelve men has a colour-vision deficiency, and a
 * red/green NEW-versus-REMOVED encoding is the single worst pairing for the commonest form.
 */
export interface DiffStyle {
  /** Spelled out on the edge — the primary cue. */
  badge: string;
  /** SVG dash pattern; undefined means solid. The secondary cue. */
  dash?: string;
  width: number;
  colour: string;
}

export function diffStyle(edge: DiffEdge): DiffStyle {
  switch (edge.classification) {
    case "NEW":
      return { badge: "NEW", dash: undefined, width: 3, colour: "var(--ok)" };
    case "REMOVED":
      // Dashed: the relationship is not there any more, and the broken line says so before any
      // colour is perceived.
      return { badge: "REMOVED", dash: "7 4", width: 2, colour: "var(--danger)" };
    case "CHANGED":
      return {
        badge: changedBadge(edge),
        dash: "2 3",
        width: 4,
        colour: "var(--warn)",
      };
    default:
      return { badge: "UNCHANGED", dash: undefined, width: 1.5, colour: "var(--edge)" };
  }
}

/** CHANGED carries its magnitude, so the badge answers "by how much" without opening a panel. */
function changedBadge(edge: DiffEdge): string {
  const percent = edge.bytes_percent_delta ?? edge.connection_percent_delta;
  if (percent === null || percent === undefined) return "CHANGED";
  const sign = percent > 0 ? "+" : "";
  return `CHANGED ${sign}${percent.toFixed(0)}%`;
}

export const DIFF_LEGEND: { classification: DiffClassification; label: string; meaning: string }[] =
  [
    { classification: "NEW", label: "NEW", meaning: "absent from the baseline period" },
    { classification: "REMOVED", label: "REMOVED", meaning: "present then, gone now" },
    { classification: "CHANGED", label: "CHANGED ±%", meaning: "crossed the change threshold" },
    { classification: "UNCHANGED", label: "UNCHANGED", meaning: "within the threshold" },
  ];
