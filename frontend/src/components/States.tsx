/**
 * Loading, empty, error, and truncated states.
 *
 * Each says what happened and what to do next. An empty graph is the NORMAL first state for this
 * product — nothing has communicated yet — so it must read as an instruction, not a failure.
 */

export function LoadingState() {
  return (
    <div className="state" role="status">
      <div className="state__skeleton" aria-hidden="true">
        <span /><span /><span />
      </div>
      <p className="state__title">Reading connections…</p>
    </div>
  );
}

export function EmptyState({ windowLabel }: { windowLabel: string }) {
  return (
    <div className="state" role="status">
      <p className="state__title">No connections observed in the last {windowLabel}</p>
      <p className="state__body">
        This is normal when nothing has talked yet. The agent records new TCP connections, so a
        service reusing an existing connection produces nothing to show.
      </p>
      <ul className="state__steps">
        <li>Generate traffic between workloads</li>
        <li>Allow up to 20 seconds — the agent batches every 10 s, the view polls every 5 s</li>
        <li>Widen the window if the traffic was a while ago</li>
      </ul>
    </div>
  );
}

/**
 * API messages are lowercase sentence fragments — both the backend's `detail` and the client's
 * own explanations — because they are usually composed into a larger sentence. Here they follow a
 * full stop, so the first letter is raised rather than reading as "reading. the backend is...".
 */
function asSentence(message: string): string {
  return message.charAt(0).toUpperCase() + message.slice(1);
}

/** Shown as a banner, never replacing the graph: the last good topology stays on screen. */
export function ErrorBanner({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="banner banner--error" role="alert">
      <span className="banner__text">
        Showing the last successful reading. {asSentence(message)}
      </span>
      <button type="button" onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

/**
 * Shown when the graph was too large to draw.
 *
 * Past roughly 300 edges this canvas stops responding rather than slowing down
 * (docs/limitations.md §4.1), so the alternative to this banner is a locked tab. Saying what was
 * left out, and that it was the quietest traffic, is the honest version of a limitation that
 * cannot yet be engineered away.
 */
export function RenderBudgetBanner({
  shownEdges,
  totalEdges,
  hiddenNodes,
}: {
  shownEdges: number;
  totalEdges: number;
  hiddenNodes: number;
}) {
  return (
    <div className="banner banner--warn" role="status">
      <span className="banner__text">
        Showing the {shownEdges.toLocaleString()} busiest of {totalEdges.toLocaleString()} edges
        {hiddenNodes > 0 && <> and hiding {hiddenNodes.toLocaleString()} nodes</>}. Drawing them
        all would stop the browser responding. Ranking uses successful connections; failed-only
        relationships may be hidden. Use namespace grouping, focus a workload, or
        shorten the window.
      </span>
    </div>
  );
}

export function TruncationBanner({ reason }: { reason: string }) {
  return (
    <div className="banner banner--warn" role="status">
      <span className="banner__text">{reason}</span>
    </div>
  );
}
