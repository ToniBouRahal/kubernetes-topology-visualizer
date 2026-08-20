import type { GraphSummary, TimeWindow } from "../api/types";

function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function spanLabel(window: TimeWindow): string {
  const ms = new Date(window.end).getTime() - new Date(window.start).getTime();
  const minutes = Math.round(ms / 60000);
  if (minutes < 60) return `${minutes} min`;
  const hours = minutes / 60;
  return Number.isInteger(hours) ? `${hours} h` : `${hours.toFixed(1)} h`;
}

/**
 * The observation window, drawn as a measured span.
 *
 * Every number on this screen is scoped to this window — a connection count means "in this
 * span", not "ever". Making the window a literal measured bar states that continuously, instead
 * of leaving it implied by a dropdown the reader has to remember they set.
 */
export function WindowStrip({
  window,
  summary,
  lastUpdated,
  live,
}: {
  window: TimeWindow;
  summary: GraphSummary;
  lastUpdated: Date | null;
  live: boolean;
}) {
  return (
    <div className="strip" aria-label="Observation window">
      <span className="label">Observed</span>

      <span className="strip__bounds mono">{clock(window.start)}</span>
      <span className="strip__rule" aria-hidden="true">
        <span className="strip__tick" />
        <span className="strip__span">{spanLabel(window)}</span>
        <span className="strip__tick" />
      </span>
      <span className="strip__bounds mono">{clock(window.end)}</span>

      <span className="strip__divider" aria-hidden="true" />

      {/* Counts are readings, so they are monospace and tabular. */}
      <span className="strip__stat">
        <b className="mono">{summary.node_count}</b> nodes
      </span>
      <span className="strip__stat">
        <b className="mono">{summary.edge_count}</b> edges
      </span>
      <span className="strip__stat" title="TCP connection establishments, not requests">
        <b className="mono">{summary.total_connections}</b> connections
      </span>

      <span className="strip__spacer" />

      {lastUpdated && (
        <span className="strip__updated">
          {live ? "updated" : "frozen at"} <span className="mono">{lastUpdated.toLocaleTimeString()}</span>
        </span>
      )}
    </div>
  );
}
