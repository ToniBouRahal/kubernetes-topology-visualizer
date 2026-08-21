import type { DiffSummary } from "../../api/types";
import { DIFF_LEGEND, diffStyle } from "../graph/diffEncoding";
import { COMPARE_SPANS, describePeriods, type ComparePeriods, type CompareSpanId } from "./periods";

/** A miniature of the real edge treatment, so the legend shows what the canvas draws. */
function DiffSwatch({ classification }: { classification: string }) {
  const style = diffStyle({ classification } as never);
  return (
    <svg width={34} height={10} viewBox="0 0 34 10" aria-hidden="true" className="swatch">
      <line
        x1="1"
        y1="5"
        x2="33"
        y2="5"
        stroke={style.colour}
        strokeWidth={style.width}
        strokeDasharray={style.dash}
      />
    </svg>
  );
}

export function CompareControls({
  span,
  onSpanChange,
  periods,
  includeUnchanged,
  onToggleUnchanged,
  summary,
  threshold,
  onRefresh,
  loading,
}: {
  span: CompareSpanId;
  onSpanChange: (id: CompareSpanId) => void;
  periods: ComparePeriods;
  includeUnchanged: boolean;
  onToggleUnchanged: () => void;
  summary: DiffSummary | null;
  threshold: number | null;
  onRefresh: () => void;
  loading: boolean;
}) {
  return (
    <section className="compare" aria-label="Comparison controls">
      <div className="compare__row">
        <span className="label">Compare</span>
        <label>
          <span className="visually-hidden">Period length</span>
          <select value={span} onChange={(e) => onSpanChange(e.target.value as CompareSpanId)}>
            {COMPARE_SPANS.map((s) => (
              <option key={s.id} value={s.id}>
                {s.label} vs previous {s.label}
              </option>
            ))}
          </select>
        </label>
        <button type="button" onClick={onRefresh} disabled={loading}>
          {loading ? "Comparing…" : "Compare"}
        </button>
      </div>

      {/* The exact periods, stated. A comparison whose windows are implied is a comparison the
          reader cannot check. */}
      <p className="compare__periods mono">{describePeriods(periods)}</p>

      {summary && (
        <ul className="compare__counts">
          <li>
            <b className="mono">{summary.new_count}</b> new
          </li>
          <li>
            <b className="mono">{summary.removed_count}</b> removed
          </li>
          <li>
            <b className="mono">{summary.changed_count}</b> changed
          </li>
        </ul>
      )}

      <label className="check">
        <input type="checkbox" checked={includeUnchanged} onChange={onToggleUnchanged} />
        <span>Show unchanged</span>
      </label>

      <ul className="legend">
        {DIFF_LEGEND.map((entry) => (
          <li key={entry.classification} className="legend__row">
            <DiffSwatch classification={entry.classification} />
            <span className="legend__name">{entry.label}</span>
          </li>
        ))}
      </ul>

      {threshold !== null && (
        <p className="panel__hint">
          An edge counts as changed at <b className="mono">{threshold}%</b> or more. Line pattern
          and the label both carry the classification, so the view reads without colour.
        </p>
      )}
    </section>
  );
}
