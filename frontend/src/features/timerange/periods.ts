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
  if (periodsProblem(periods) !== null && !periods.baselineFrom) {
    return "—";
  }
  const sameDay =
    new Date(periods.baselineFrom).toDateString() === new Date(periods.currentFrom).toDateString();
  const time = (iso: string) =>
    new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  // Two moments weeks apart read identically to two moments minutes apart if only the clock time
  // is shown, which is exactly the comparison a reader most needs to be able to check. The date
  // appears as soon as the periods fall on different days.
  const stamp = (iso: string) =>
    sameDay
      ? time(iso)
      : `${new Date(iso).toLocaleDateString([], { day: "2-digit", month: "short" })} ${time(iso)}`;
  return `${stamp(periods.baselineFrom)}–${time(periods.baselineTo)} vs ${stamp(periods.currentFrom)}–${time(periods.currentTo)}`;
}

/**
 * Two chosen moments, each opening a window of the SAME length.
 *
 * Length is shared rather than picked per period on purpose. The diff API accepts periods of
 * different lengths, and comparing a five-minute window against a six-hour one is the easiest way
 * to produce a confidently wrong answer in this whole interface: connection counts are totals, not
 * rates, so the longer period wins every comparison and every edge reads as a large increase. One
 * span for both sides makes that unrepresentable rather than merely discouraged.
 */
export function periodsFromMoments(
  baselineStart: Date,
  currentStart: Date,
  spanMinutes: number,
): ComparePeriods {
  const ms = spanMinutes * 60_000;
  // An emptied date input parses to an Invalid Date, whose toISOString THROWS. That throw would
  // escape the change handler and take the panel down, so a half-typed date must degrade to
  // something periodsProblem can report rather than to an exception.
  const iso = (moment: Date, offset = 0) => {
    const at = moment.getTime() + offset;
    return Number.isNaN(at) ? "" : new Date(at).toISOString();
  };

  return {
    baselineFrom: iso(baselineStart),
    baselineTo: iso(baselineStart, ms),
    currentFrom: iso(currentStart),
    currentTo: iso(currentStart, ms),
  };
}

/**
 * Why a chosen pair of periods cannot be compared, or null when it can.
 *
 * Checked here rather than left to the backend's 422 so the reason appears next to the inputs
 * that caused it. The overlap rule is the backend's (ADR-003 D-3.6) and is restated, not invented:
 * a shared interval would be counted on both sides and make CHANGED meaningless.
 */
export function periodsProblem(periods: ComparePeriods): string | null {
  const bFrom = new Date(periods.baselineFrom).getTime();
  const bTo = new Date(periods.baselineTo).getTime();
  const cFrom = new Date(periods.currentFrom).getTime();
  const cTo = new Date(periods.currentTo).getTime();

  if ([bFrom, bTo, cFrom, cTo].some(Number.isNaN)) {
    return "Both moments need a date and a time.";
  }
  if (bFrom < cTo && cFrom < bTo) {
    return "The two periods overlap. The shared interval would be counted on both sides, which makes a changed edge meaningless — move them apart or shorten the period.";
  }
  return null;
}

/**
 * The value a datetime-local input wants: local wall-clock, no zone, minute precision.
 *
 * toISOString would be UTC and the browser would render it as a different wall-clock time than
 * the one that was picked, silently shifting every comparison by the viewer's offset.
 */
export function toLocalInputValue(moment: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${moment.getFullYear()}-${pad(moment.getMonth() + 1)}-${pad(moment.getDate())}` +
    `T${pad(moment.getHours())}:${pad(moment.getMinutes())}`
  );
}
