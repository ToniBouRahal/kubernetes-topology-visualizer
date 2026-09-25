import type { WindowPreset } from "../../api/types";
import { toLocalInputValue } from "./periods";

/** Each preset's length. The same lengths as Live, so a window read in one mode means the same in the other. */
export const PRESET_MINUTES: Record<WindowPreset, number> = {
  "1m": 1,
  "5m": 5,
  "15m": 15,
  "1h": 60,
  "6h": 360,
  "24h": 1440,
};

/** "5 min", "1 h": a length, where Live's select reads "last 5m", a position relative to now. */
export function lengthLabel(preset: WindowPreset): string {
  const minutes = PRESET_MINUTES[preset];
  return minutes < 60 ? `${minutes} min` : `${minutes / 60} h`;
}

export type HistoryRange = { from: string; to: string; problem: null } | { from: null; to: null; problem: string };

/**
 * The fixed period History shows: a chosen start and the preset's length.
 *
 * A start plus a length rather than two free ends, so it matches Live's window lengths exactly
 * and cannot be dragged into a span the backend would reject for being too long.
 */
export function historyRange(startLocal: string, preset: WindowPreset, now: Date = new Date()): HistoryRange {
  const start = new Date(startLocal).getTime();
  if (!startLocal || Number.isNaN(start)) return { from: null, to: null, problem: "Pick a date and a time to start from." };
  if (start >= now.getTime()) return { from: null, to: null, problem: "That moment has not happened yet." };
  return {
    problem: null,
    from: new Date(start).toISOString(),
    to: new Date(start + PRESET_MINUTES[preset] * 60_000).toISOString(),
  };
}

/** Where History opens: the window Live was just showing, now held still. */
export function initialHistoryStart(preset: WindowPreset, now: Date = new Date()): string {
  const start = new Date(now.getTime() - PRESET_MINUTES[preset] * 60_000);
  start.setSeconds(0, 0);
  return toLocalInputValue(start);
}

/**
 * One window earlier (-1) or later (+1).
 *
 * Stepping later never runs past now: the last step lands on the window ending at the current
 * minute rather than on a stretch of future with nothing in it.
 */
export function stepHistoryStart(startLocal: string, preset: WindowPreset, direction: -1 | 1, now: Date = new Date()): string {
  const span = PRESET_MINUTES[preset] * 60_000;
  const start = new Date(startLocal).getTime();
  if (Number.isNaN(start)) return initialHistoryStart(preset, now);
  const latest = new Date(now.getTime() - span);
  latest.setSeconds(0, 0);
  return toLocalInputValue(new Date(Math.min(start + direction * span, latest.getTime())));
}

/** True when a later window would reach past now, so there is nothing to step forward to. */
export function atLatest(startLocal: string, preset: WindowPreset, now: Date = new Date()): boolean {
  const start = new Date(startLocal).getTime();
  return Number.isNaN(start) || start + PRESET_MINUTES[preset] * 60_000 >= now.getTime() - 60_000;
}
