import { describe, expect, it } from "vitest";

import {
  adjacentPeriods,
  describePeriods,
  periodsFromMoments,
  periodsProblem,
  toLocalInputValue,
} from "../src/features/timerange/periods";

/**
 * Comparison period arithmetic.
 *
 * These are pure functions over dates, and every one of them has a failure mode that produces a
 * plausible-looking wrong answer rather than an error: a window shifted by the viewer's UTC
 * offset, two periods that silently overlap, or two periods of different lengths whose counts are
 * not comparable. None of those announce themselves in the interface.
 */

describe("adjacent periods (recent vs previous)", () => {
  const now = new Date("2026-09-12T12:00:00.000Z");

  it("produces two windows that abut without overlapping", () => {
    const p = adjacentPeriods(5, now);

    expect(p.baselineTo).toBe(p.currentFrom);
    expect(periodsProblem(p)).toBeNull();
  });

  it("gives both periods the same length", () => {
    const p = adjacentPeriods(15, now);
    const baseline = +new Date(p.baselineTo) - +new Date(p.baselineFrom);
    const current = +new Date(p.currentTo) - +new Date(p.currentFrom);

    expect(baseline).toBe(current);
    expect(current).toBe(15 * 60_000);
  });

  it("ends the current period at now", () => {
    expect(adjacentPeriods(5, now).currentTo).toBe(now.toISOString());
  });
});

describe("periods from two chosen moments", () => {
  it("opens a window of the requested length at each moment", () => {
    const p = periodsFromMoments(
      new Date("2026-08-06T09:15:00.000Z"),
      new Date("2026-09-10T09:15:00.000Z"),
      15,
    );

    expect(p.baselineFrom).toBe("2026-08-06T09:15:00.000Z");
    expect(p.baselineTo).toBe("2026-08-06T09:30:00.000Z");
    expect(p.currentFrom).toBe("2026-09-10T09:15:00.000Z");
    expect(p.currentTo).toBe("2026-09-10T09:30:00.000Z");
  });

  /**
   * The reason the span is shared rather than picked per period. Connection counts are totals,
   * not rates, so an unequal comparison makes the longer period win every edge — a wrong answer
   * that looks like a finding.
   */
  it("gives both periods the same length whatever the gap between them", () => {
    const p = periodsFromMoments(
      new Date("2026-08-06T09:15:00.000Z"),
      new Date("2026-09-10T09:15:00.000Z"),
      60,
    );
    const baseline = +new Date(p.baselineTo) - +new Date(p.baselineFrom);
    const current = +new Date(p.currentTo) - +new Date(p.currentFrom);

    expect(baseline).toBe(current);
  });

  it("accepts periods weeks apart, in either order", () => {
    const older = new Date("2026-08-06T09:15:00.000Z");
    const newer = new Date("2026-09-10T09:15:00.000Z");

    expect(periodsProblem(periodsFromMoments(older, newer, 15))).toBeNull();
    // Baseline AFTER the compared period is legal: "what changed going backwards" is a question
    // the API accepts, and refusing it here would be the UI inventing a rule.
    expect(periodsProblem(periodsFromMoments(newer, older, 15))).toBeNull();
  });
});

describe("rejecting period pairs the backend would refuse", () => {
  it("rejects periods that overlap", () => {
    const p = periodsFromMoments(
      new Date("2026-09-12T09:00:00.000Z"),
      new Date("2026-09-12T09:30:00.000Z"),
      60, // each period is an hour, so they overlap by 30 minutes
    );

    expect(periodsProblem(p)).toMatch(/overlap/i);
  });

  it("accepts periods that merely touch", () => {
    // Windows are half-open, so the boundary instant belongs to exactly one period.
    const p = periodsFromMoments(
      new Date("2026-09-12T09:00:00.000Z"),
      new Date("2026-09-12T10:00:00.000Z"),
      60,
    );

    expect(periodsProblem(p)).toBeNull();
  });

  it("rejects an incomplete moment rather than sending NaN to the API", () => {
    const p = periodsFromMoments(new Date(""), new Date("2026-09-12T10:00:00.000Z"), 15);

    expect(periodsProblem(p)).toMatch(/date and a time/i);
  });
});

describe("what the reader is told", () => {
  it("shows the date once the periods fall on different days", () => {
    const described = describePeriods(
      periodsFromMoments(
        new Date("2026-08-06T09:15:00.000Z"),
        new Date("2026-09-10T09:15:00.000Z"),
        15,
      ),
    );

    // Clock time alone would render two moments five weeks apart identically, which is exactly
    // the comparison the reader most needs to be able to check.
    expect(described).toMatch(/Aug/);
    expect(described).toMatch(/Sep/);
  });

  it("stays on clock time when both periods are the same day", () => {
    const described = describePeriods(adjacentPeriods(5, new Date("2026-09-12T12:00:00.000Z")));

    expect(described).not.toMatch(/Aug|Sep/);
  });
});

describe("the datetime-local value", () => {
  /**
   * toISOString would hand the input a UTC string, and the browser would then display a different
   * wall-clock time than the one that was picked — every comparison silently shifted by the
   * viewer's offset, with nothing anywhere reporting an error.
   */
  it("is the viewer's local wall-clock, not UTC", () => {
    const moment = new Date(2026, 8, 10, 9, 15); // local 09:15 on 10 Sep

    expect(toLocalInputValue(moment)).toBe("2026-09-10T09:15");
  });

  it("pads every field to the width the input requires", () => {
    expect(toLocalInputValue(new Date(2026, 0, 2, 3, 4))).toBe("2026-01-02T03:04");
  });
});
