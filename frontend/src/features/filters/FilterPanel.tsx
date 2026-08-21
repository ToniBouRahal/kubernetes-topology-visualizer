import type React from "react";

import { KIND_ENCODING, encodingFor, namespaceHue, shapePath } from "../graph/encoding";

const LEGEND_KINDS = ["Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "Pod", "External"];

/** A miniature of the real node outline, so the legend shows the same mark the canvas draws. */
function ShapeSwatch({ kind }: { kind: string }) {
  const encoding = encodingFor(kind);
  const w = 30;
  const h = 16;
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden="true" className="swatch">
      <path
        d={shapePath(encoding.shape, w, h)}
        fill="var(--panel)"
        stroke="var(--text-dim)"
        strokeWidth={1.25}
        strokeDasharray={encoding.shape === "dashed-pill" ? "3 2.5" : undefined}
      />
    </svg>
  );
}

export function FilterPanel({
  namespaces,
  selectedNamespaces,
  onToggleNamespace,
  search,
  onSearch,
  includeExternal,
  onToggleExternal,
  onClear,
  extra,
}: {
  namespaces: string[];
  selectedNamespaces: string[];
  onToggleNamespace: (ns: string) => void;
  search: string;
  onSearch: (value: string) => void;
  includeExternal: boolean;
  onToggleExternal: () => void;
  onClear: () => void;
  /** Mode-specific controls rendered above the filters (compare periods, for example). */
  extra?: React.ReactNode;
}) {
  const filtering = selectedNamespaces.length > 0 || search !== "" || !includeExternal;

  return (
    <aside className="panel panel--left" aria-label="Filters">
      {extra}
      <section className="panel__section">
        <label className="label" htmlFor="search">
          Search
        </label>
        <input
          id="search"
          type="search"
          value={search}
          placeholder="service or workload"
          onChange={(e) => onSearch(e.target.value)}
        />
      </section>

      <section className="panel__section">
        <span className="label">Namespaces</span>
        {namespaces.length === 0 ? (
          <p className="panel__hint">None observed yet.</p>
        ) : (
          <ul className="ns-list">
            {namespaces.map((ns) => {
              const on = selectedNamespaces.includes(ns);
              return (
                <li key={ns}>
                  <button
                    type="button"
                    className={`ns ${on ? "ns--on" : ""}`}
                    aria-pressed={on}
                    onClick={() => onToggleNamespace(ns)}
                  >
                    <span className="ns__chip" style={{ background: namespaceHue(ns) }} aria-hidden="true" />
                    <span className="ns__name mono">{ns}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        {/* Filtering by namespace keeps edges that LEAVE it, so a dependency on another
            namespace stays visible. Said plainly, because the alternative reading is reasonable. */}
        <p className="panel__hint">Shows traffic to and from the selected namespaces.</p>
      </section>

      <section className="panel__section">
        <label className="check">
          <input type="checkbox" checked={includeExternal} onChange={onToggleExternal} />
          <span>Show external</span>
        </label>
      </section>

      <section className="panel__section panel__section--legend">
        <span className="label">Node kinds</span>
        <ul className="legend">
          {LEGEND_KINDS.map((kind) => (
            <li key={kind} className="legend__row">
              <ShapeSwatch kind={kind} />
              <span className="legend__name">{KIND_ENCODING[kind]?.label ?? kind}</span>
            </li>
          ))}
        </ul>
        <p className="panel__hint">
          Shape shows kind, colour shows namespace. Edge thickness shows{" "}
          <b>connection count</b> — TCP establishments, not requests.
        </p>
      </section>

      {filtering && (
        <button type="button" className="panel__clear" onClick={onClear}>
          Clear filters
        </button>
      )}
    </aside>
  );
}
