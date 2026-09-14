import type React from "react";

import { namespaceHue } from "../graph/encoding";

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
  nodeList,
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
  /** Keyboard-navigable node list — the accessible equivalent of clicking the canvas. */
  nodeList?: React.ReactNode;
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

      {nodeList}

      {/* The shape key is gone: every node is drawn the same way and the kind is written on the
          node itself. What survives is the one claim a viewer cannot infer from the picture and
          would otherwise get wrong — thickness is connections, not requests (docs/demo-script.md
          §4 asks for this line to be on screen). */}
      <section className="panel__section panel__section--legend">
        <p className="panel__hint">
          Colour shows namespace. Edge thickness shows <b>successful TCP establishments</b>. Dashed amber edges include failed/aborted attempts.
          Mean TCP setup measures successful establishment time. Failure counts cover measured observations;
          older data may be unmeasured.
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
