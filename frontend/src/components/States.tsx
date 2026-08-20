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

/** Shown as a banner, never replacing the graph: the last good topology stays on screen. */
export function ErrorBanner({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="banner banner--error" role="alert">
      <span className="banner__text">
        Showing the last successful reading. {message}
      </span>
      <button type="button" onClick={onRetry}>
        Retry
      </button>
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
