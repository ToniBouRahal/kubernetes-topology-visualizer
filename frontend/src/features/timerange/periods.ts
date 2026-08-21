/**
 * Comparison period arithmetic.
 *
 * Kept separate from the components so the rules are testable: two adjacent, non-overlapping
 * windows of equal length. The backend rejects overlap outright, because a shared interval would
 * be counted on both sides and make a CHANGED classification meaningless (ADR-003 D-3.6).
 */

export const COMPARE_SPANS = [
  { id: "5m", label: "5 minutes", minutes: 5 },
  { id: "15m", label: "15 minutes", minutes: 15 },
  { id: "1h", label: "1 hour", minutes: 60 },
  { id: "6h", label: "6 hours", minutes: 360 },
] as const;

export type CompareSpanId = (typeof COMPARE_SPANS)[number]["id"];

export interface ComparePeriods {
  baselineFrom: string;
  baselineTo: string;
  currentFrom: string;
  currentTo: string;
}

/**
 * Two back-to-back windows ending now: [now-2n, now-n) and [now-n, now).
 *
 * They ABUT rather than overlap — the baseline ends exactly where the current period starts.
 * That is legal because windows are half-open, so the boundary instant belongs to exactly one
 * of them (ADR-005 D-5.4).
 */
export function adjacentPeriods(spanMinutes: number, now: Date = new Date()): ComparePeriods {
  const ms = spanMinutes * 60_000;
  const end = now.getTime();
  const mid = end - ms;
  const start = mid - ms;

  return {
    baselineFrom: new Date(start).toISOString(),
    baselineTo: new Date(mid).toISOString(),
    currentFrom: new Date(mid).toISOString(),
    currentTo: new Date(end).toISOString(),
  };
}

export function describePeriods(periods: ComparePeriods): string {
  const time = (iso: string) =>
    new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return `${time(periods.baselineFrom)}–${time(periods.baselineTo)} vs ${time(periods.currentFrom)}–${time(periods.currentTo)}`;
}
