import type { Anomaly, AnomalyResponse, AnomalyType } from "../../api/types";

/** The rule, in the words a reader would use for it (ADR-016 D-16.1). */
export const ANOMALY_LABELS: Record<AnomalyType, string> = {
  CANARY_CONTACT: "Canary contacted",
  NEW_EXTERNAL_EGRESS: "New external egress",
  NEW_EDGE: "New relationship",
  FAILURE_SPIKE: "Failure spike",
  SLOW_SETUP: "Slower TCP setup",
};

/** Hours between two RFC 3339 instants, for "the previous N h". */
export function baselineHours(response: AnomalyResponse): number {
  const ms = Date.parse(response.baseline.end) - Date.parse(response.baseline.start);
  return Math.round(ms / 3_600_000);
}

/**
 * Names for the two ends of an anomaly, from the response's own node records.
 *
 * The ids are opaque (contracts/ids.md §2); where a record is missing the id is shown verbatim
 * rather than taken apart for something that looks like a name.
 */
export function anomalyEnds(response: AnomalyResponse, anomaly: Anomaly): [string, string] {
  const labels = new Map(response.nodes.map((n) => [n.id, n.label]));
  return [
    labels.get(anomaly.source_id) ?? anomaly.source_id,
    labels.get(anomaly.target_id) ?? anomaly.target_id,
  ];
}

/**
 * The anomaly section of the left panel.
 *
 * Severity is written as a word, not carried by colour alone (ADR-006 D-6.7), and every item
 * repeats the backend's reason — the numbers compared and the threshold — so a finding is never
 * a bare assertion. Selecting one selects its source: that is the workload whose behaviour
 * changed, and the details panel then shows what it talks to.
 */
export function AnomalyList({
  response,
  onSelect,
}: {
  response: AnomalyResponse | null;
  onSelect: (nodeId: string) => void;
}) {
  if (!response) return null;
  const hours = baselineHours(response);
  const { anomalies, summary } = response;

  return (
    <section className="panel__section">
      <span className="label" id="anomaly-list-label">
        Anomalies <span className="mono">({anomalies.length})</span>
      </span>

      {!summary.baseline_observed && (
        // D-16.2: without a baseline every edge would be "new", so those rules are off — said in
        // words, because an empty list here would otherwise read as "nothing unusual".
        <p className="panel__hint">
          No baseline yet — new-relationship rules are off until the previous period has data.
        </p>
      )}

      {anomalies.length === 0 ? (
        <p className="panel__hint">No anomalies against the previous {hours} h.</p>
      ) : (
        <ul className="anomaly-list" aria-labelledby="anomaly-list-label">
          {anomalies.map((anomaly) => {
            const [source, target] = anomalyEnds(response, anomaly);
            return (
              <li key={anomaly.id}>
                <button
                  type="button"
                  className={`anomaly anomaly--${anomaly.severity}`}
                  onClick={() => onSelect(anomaly.source_id)}
                >
                  <span className="anomaly__head">
                    <span className="anomaly__severity">{anomaly.severity}</span>
                    <span className="anomaly__type">{ANOMALY_LABELS[anomaly.type]}</span>
                  </span>
                  <span className="anomaly__pair mono">
                    {source} → {target} :{anomaly.destination_port}
                  </span>
                  <span className="anomaly__reason">{anomaly.reason}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
      {summary.truncated && (
        <p className="panel__hint">More anomalies than the response cap; narrow the filters.</p>
      )}
    </section>
  );
}
