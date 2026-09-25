import { describe, expect, it } from "vitest";

import { atLatest, historyRange, initialHistoryStart, lengthLabel, stepHistoryStart } from "../src/features/timerange/history";
import { toLocalInputValue } from "../src/features/timerange/periods";

// Local wall-clock, as a datetime-local input produces it.
const now = new Date(2026, 8, 25, 20, 30, 42);

describe("history range", () => {
  it("is the chosen start plus the preset's length, sent as UTC", () => {
    const range = historyRange("2026-09-24T14:00", "1h", now);
    expect(range.problem).toBeNull();
    if (range.problem !== null) return;
    expect(new Date(range.to).getTime() - new Date(range.from).getTime()).toBe(60 * 60_000);
    // The same wall-clock moment the user picked, whatever their zone.
    expect(new Date(range.from).getTime()).toBe(new Date(2026, 8, 24, 14, 0).getTime());
  });

  it("says why a range cannot be shown instead of querying it", () => {
    expect(historyRange("", "5m", now).problem).toMatch(/date and a time/);
    expect(historyRange("2026-09-26T09:00", "5m", now).problem).toMatch(/not happened yet/);
  });

  it("opens on the window Live was just showing", () => {
    expect(initialHistoryStart("5m", now)).toBe(toLocalInputValue(new Date(2026, 8, 25, 20, 25)));
  });

  it("steps a whole window at a time, and never past now", () => {
    expect(stepHistoryStart("2026-09-25T18:00", "1h", -1, now)).toBe("2026-09-25T17:00");
    expect(stepHistoryStart("2026-09-25T18:00", "1h", 1, now)).toBe("2026-09-25T19:00");
    // 19:30 + 1 h would end at 21:30; the latest start is the window ending now.
    expect(stepHistoryStart("2026-09-25T19:30", "1h", 1, now)).toBe("2026-09-25T19:30");
    expect(atLatest("2026-09-25T19:30", "1h", now)).toBe(true);
    expect(atLatest("2026-09-25T18:00", "1h", now)).toBe(false);
  });

  it("labels a length, not a position relative to now", () => {
    expect(lengthLabel("5m")).toBe("5 min");
    expect(lengthLabel("6h")).toBe("6 h");
  });
});
