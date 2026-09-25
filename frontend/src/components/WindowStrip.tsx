import type { GraphSummary, TimeWindow } from "../api/types";

function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

/** A moment in History: the date as well as the time, since the window can be days back. */
function stamp(iso: string): string {
  const at = new Date(iso);
  const time = at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return at.toDateString() === new Date().toDateString()
    ? `today ${time}`
    : `${at.toLocaleDateString([], { weekday: "short", day: "2-digit", month: "short" })} ${time}`;
}

function spanLabel(window: TimeWindow): string {
  const ms = new Date(window.end).getTime() - new Date(window.start).getTime();
  const minutes = Math.round(ms / 60000);
  if (minutes < 60) return `${minutes} min`;
  const hours = minutes / 60;
  return Number.isInteger(hours) ? `${hours} h` : `${hours.toFixed(1)} h`;
}

export type StripMode = "live" | "paused" | "history";

/** The controls History adds to the strip. */
export interface HistoryControls {
  /** datetime-local value: the viewer's wall-clock, minute precision. */
  start: string;
  onStartChange: (value: string) => void;
  onStep: (direction: -1 | 1) => void;
  /** False when the window already ends at the current minute. */
  canStepLater: boolean;
  /** Why the chosen start cannot be shown; the graph keeps the last period that could. */
  problem: string | null;
  onBackToLive: () => void;
}

/**
 * The observation window, drawn as a measured span, and the one place that says which mode this is.
 *
 * Every number on this screen is scoped to this window — a connection count means "in this
 * span", not "ever". Making the window a literal measured bar states that continuously, instead
 * of leaving it implied by a dropdown the reader has to remember they set.
 *
 * Live and History must never look alike: an old reading mistaken for the current one answers
 * "what is talking right now?" wrongly and confidently. Live says LIVE, with the only moving mark
 * on the strip; History says HISTORY in the other accent, shows full dates, and offers the way
 * back. The accent itself is swapped by `.app[data-mode]` in CSS (DESIGN.md), not here.
 */
export function WindowStrip({
  window,
  summary,
  lastUpdated,
  mode,
  history,
}: {
  window: TimeWindow;
  summary: GraphSummary;
  lastUpdated: Date | null;
  mode: StripMode;
  history?: HistoryControls;
}) {
  const inHistory = mode === "history" && history;

  return (
    <div className={`strip${inHistory ? " strip--history" : ""}`} aria-label="Observation window">
      {inHistory ? (
        <span className="strip__mode">History · not live</span>
      ) : (
        <span className={`strip__mode${mode === "live" ? " strip__mode--live" : ""}`}>
          <span className="strip__signal" aria-hidden="true" />
          {mode === "live" ? "Live" : "Paused"}
        </span>
      )}

      {inHistory ? (
        <>
          <button type="button" className="strip__step" onClick={() => history.onStep(-1)}
            aria-label={`Earlier: the ${spanLabel(window)} before this`}>
            Earlier
          </button>
          <label className="strip__start">
            <span className="visually-hidden">Start of the period</span>
            <input type="datetime-local" value={history.start} onChange={(e) => history.onStartChange(e.target.value)} />
          </label>
          <span className="strip__rule" aria-hidden="true">
            <span className="strip__tick" />
            <span className="strip__span">{spanLabel(window)}</span>
            <span className="strip__tick" />
          </span>
          <span className="strip__bounds mono">{stamp(window.end)}</span>
          <button type="button" className="strip__step" onClick={() => history.onStep(1)}
            disabled={!history.canStepLater} aria-label={`Later: the ${spanLabel(window)} after this`}>
            Later
          </button>
        </>
      ) : (
        <>
          <span className="strip__bounds mono">{clock(window.start)}</span>
          <span className="strip__rule" aria-hidden="true">
            <span className="strip__tick" />
            <span className="strip__span">{spanLabel(window)}</span>
            <span className="strip__tick" />
          </span>
          <span className="strip__bounds mono">{clock(window.end)}</span>
        </>
      )}

      <span className="strip__divider" aria-hidden="true" />

      {inHistory && history.problem ? (
        <span className="strip__problem" role="alert">{history.problem}</span>
      ) : (
        <>
          {/* Counts are readings, so they are monospace and tabular. */}
          <span className="strip__stat">
            <b className="mono">{summary.node_count}</b> components
          </span>
          <span className="strip__stat">
            <b className="mono">{summary.edge_count}</b> links
          </span>
          <span className="strip__stat" title="TCP connection establishments, not requests">
            <b className="mono">{summary.total_connections}</b> connections
          </span>
        </>
      )}

      <span className="strip__spacer" />

      {inHistory ? (
        <button type="button" className="strip__back" onClick={history.onBackToLive}>
          Back to live
        </button>
      ) : (
        lastUpdated && (
          <span className="strip__updated">
            {mode === "live" ? "updated" : "frozen at"} <span className="mono">{lastUpdated.toLocaleTimeString()}</span>
          </span>
        )
      )}
    </div>
  );
}
