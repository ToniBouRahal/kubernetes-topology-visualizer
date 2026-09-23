import type { GraphNode, NodeDependency, NodeDetail } from "../../api/types";
import { namespaceHue, namespaceLabel } from "../graph/encoding";

import { outcomeParts } from "../graph/outcomes";

/**
 * One observed link, stated as a direction rather than a membership.
 *
 * "Incoming" and "Outgoing" name the section, but a reader scanning a list of names cannot tell
 * which way any single row runs without re-reading the heading above it. Writing `a → b` on the
 * row itself makes each one legible on its own (ADR-010 D-10.1).
 */
function Dependency({ pair, dep }: { pair: string; dep: NodeDependency }) {
  const { successful, failed, timing } = outcomeParts(dep);
  return (
    <li className={`dep${(failed ?? 0) > 0 ? " dep--failed" : ""}`}>
      <span className="dep__pair">{pair}</span>
      <span className="dep__stats">
        <span>
          port <b className="mono">{dep.protocol}:{dep.destination_port}</b>
        </span>
        <span>
          <b className="mono">{successful}</b> successful
        </span>
        {/* Amber only when there is something to warn about: a measured zero is good news and
            must not be dressed as a fault. A null count is "never measured", which is a third
            state again — neither a failure nor a clean bill. */}
        <span className={(failed ?? 0) > 0 ? "dep__failed" : undefined}>
          <b className="mono">{failed ?? "—"}</b> {failed === null ? "failed/aborted unmeasured" : "failed/aborted"}
        </span>
      </span>
      <span className="dep__timing mono">{timing}</span>
    </li>
  );
}

function DependencyList({
  title,
  items,
  empty,
  pairFor,
}: {
  title: string;
  items: NodeDependency[];
  empty: string;
  pairFor: (dep: NodeDependency) => string;
}) {
  return (
    <section className="panel__section">
      <span className="label">
        {title} <span className="mono">({items.length})</span>
      </span>
      {items.length === 0 ? (
        <p className="panel__hint">{empty}</p>
      ) : (
        <ul className="dep-list">
          {items.map((dep) => (
            <Dependency
              key={`${dep.node_id}-${dep.destination_port}`}
              pair={pairFor(dep)}
              dep={dep}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

export function DetailsPanel({
  node,
  detail,
  loading,
  onClose,
}: {
  node: GraphNode | null;
  detail: NodeDetail | null;
  loading: boolean;
  onClose: () => void;
}) {
  if (!node) {
    return (
      <aside className="panel panel--right panel--empty" aria-label="Details">
        <p className="panel__hint">Select a component to see what it talks to.</p>
      </aside>
    );
  }

  const hue = namespaceHue(node.namespace);
  const touching = [...(detail?.incoming ?? []), ...(detail?.outgoing ?? [])];
  const totalSuccessful = touching.reduce((sum, d) => sum + d.connection_count, 0);
  const totalFailed = touching.reduce((sum, d) => sum + (d.failed_connection_count ?? 0), 0);

  return (
    <aside className="panel panel--right" aria-label={`Details for ${node.label}`}>
      <div className="panel__head">
        <div>
          <div className="panel__title">{node.label}</div>
          {/* Kind is not shown (D-10.1). Namespace is, in words as well as in the node's colour. */}
          <div className="panel__subtitle mono" style={{ color: hue }}>
            {namespaceLabel(node)}
          </div>
        </div>
        <button type="button" onClick={onClose} aria-label="Close details">
          ×
        </button>
      </div>

      {detail && (
        <section className="panel__section">
          <span className="label">Observed in this window</span>
          <div className="dep__stats">
            <span>
              <b className="mono">{touching.length}</b> links
            </span>
            <span>
              <b className="mono">{totalSuccessful}</b> successful
            </span>
            <span className={totalFailed > 0 ? "dep__failed" : undefined}>
              <b className="mono">{totalFailed}</b> failed/aborted
            </span>
          </div>
        </section>
      )}

      <section className="panel__section">
        <span className="label">First seen</span>
        <div className="mono panel__value">{new Date(node.first_seen).toLocaleString()}</div>
        <span className="label">Last seen</span>
        <div className="mono panel__value">{new Date(node.last_seen).toLocaleString()}</div>
      </section>

      {loading && <p className="panel__hint">Loading dependencies…</p>}

      {detail && (
        <>
          <DependencyList
            title="Incoming"
            items={detail.incoming}
            empty="Nothing observed connecting to this component in the window."
            pairFor={(dep) => `${dep.label} → ${node.label}`}
          />
          <DependencyList
            title="Outgoing"
            items={detail.outgoing}
            empty="This component opened no connections in the window."
            pairFor={(dep) => `${node.label} → ${dep.label}`}
          />
          {/* A Deployment fronted by a Service shows its inbound traffic on the SERVICE node,
              because that is what clients actually connect to. Without this note an empty
              "Incoming" reads as a bug rather than as the resolution rule working. */}
          {detail.incoming.length === 0 && node.kind !== "Service" && (
            <p className="panel__hint panel__hint--note">
              Clients usually connect to a Service rather than to a workload directly. If a
              Service fronts this node, its inbound traffic appears there.
            </p>
          )}
        </>
      )}
    </aside>
  );
}
