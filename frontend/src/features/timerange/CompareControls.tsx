import type { DiffSummary } from "../../api/types";
import { DIFF_LEGEND, diffStyle } from "../graph/diffEncoding";
import {
  COMPARE_SPANS,
  describePeriods,
  periodsProblem,
  type ComparePeriods,
  type CompareSpanId,
} from "./periods";

/** How the two periods are chosen. */
export type CompareMode = "recent" | "moments";

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
  mode,
  onModeChange,
  span,
  onSpanChange,
  baselineStart,
  currentStart,
  onBaselineStartChange,
  onCurrentStartChange,
  periods,
  includeUnchanged,
  onToggleUnchanged,
  summary,
  threshold,
  onRefresh,
  loading,
}: {
  mode: CompareMode;
  onModeChange: (mode: CompareMode) => void;
  span: CompareSpanId;
  onSpanChange: (id: CompareSpanId) => void;
  /** datetime-local values: local wall-clock, minute precision. */
  baselineStart: string;
  currentStart: string;
  onBaselineStartChange: (value: string) => void;
  onCurrentStartChange: (value: string) => void;
  periods: ComparePeriods;
  includeUnchanged: boolean;
  onToggleUnchanged: () => void;
  summary: DiffSummary | null;
  threshold: number | null;
  onRefresh: () => void;
  loading: boolean;
}) {
  const problem = mode === "moments" ? periodsProblem(periods) : null;

  return (
    <section className="compare" aria-label="Comparison controls">
      <div className="compare__row">
        <span className="label">Compare</span>
        <label>
          <span className="visually-hidden">How to choose the periods</span>
          <select value={mode} onChange={(e) => onModeChange(e.target.value as CompareMode)}>
            <option value="recent">Recent vs previous</option>
            <option value="moments">Two points in time</option>
          </select>
        </label>
      </div>

      <div className="compare__row">
        <label>
          <span className="visually-hidden">Period length</span>
          <select value={span} onChange={(e) => onSpanChange(e.target.value as CompareSpanId)}>
            {COMPARE_SPANS.map((s) => (
              <option key={s.id} value={s.id}>
                {mode === "recent" ? `${s.label} vs previous ${s.label}` : `${s.label} each`}
              </option>
            ))}
          </select>
        </label>
        <button type="button" onClick={onRefresh} disabled={loading || problem !== null}>
          {loading ? "Comparing…" : "Compare"}
        </button>
      </div>

      {mode === "moments" && (
        <div className="compare__moments">
          {/* One length for both periods, so the two sides stay comparable. Connection counts are
              totals rather than rates, so a longer period would win every comparison. */}
          <label className="compare__moment">
            <span className="label">Baseline starts</span>
            <input
              type="datetime-local"
              value={baselineStart}
              onChange={(e) => onBaselineStartChange(e.target.value)}
            />
          </label>
          <label className="compare__moment">
            <span className="label">Compared with</span>
            <input
              type="datetime-local"
              value={currentStart}
              onChange={(e) => onCurrentStartChange(e.target.value)}
            />
          </label>
        </div>
      )}

      {/* The exact periods, stated. A comparison whose windows are implied is a comparison the
          reader cannot check. */}
      <p className="compare__periods mono">{describePeriods(periods)}</p>

      {problem && (
        <p className="compare__problem" role="alert">
          {problem}
        </p>
      )}

      {mode === "moments" && !problem && (
        <p className="panel__hint">
          History reaches back only as far as the backend&rsquo;s retention window. A period older
          than that is not missing data — it was deleted, and returns an empty comparison.
        </p>
      )}

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
