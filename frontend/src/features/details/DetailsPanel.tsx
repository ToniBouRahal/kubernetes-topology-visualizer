import type { GraphNode, NodeDependency, NodeDetail } from "../../api/types";
import { encodingFor, namespaceHue } from "../graph/encoding";

function DependencyList({ title, items, empty }: { title: string; items: NodeDependency[]; empty: string }) {
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
            <li key={`${dep.node_id}-${dep.destination_port}`} className="dep">
              <span className="dep__name">{dep.label}</span>
              <span className="dep__meta mono">
                {dep.protocol}:{dep.destination_port} · {dep.connection_count}
              </span>
            </li>
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
        <p className="panel__hint">Select a node to see what it talks to.</p>
      </aside>
    );
  }

  const encoding = encodingFor(node.kind);
  const hue = namespaceHue(node.namespace);

  return (
    <aside className="panel panel--right" aria-label={`Details for ${node.label}`}>
      <div className="panel__head">
        <div>
          <div className="panel__title">{node.label}</div>
          <div className="panel__subtitle">
            <span style={{ color: hue }}>{encoding.label}</span>
            {node.namespace && <span className="mono"> · {node.namespace}</span>}
          </div>
        </div>
        <button type="button" onClick={onClose} aria-label="Close details">
          ×
        </button>
      </div>

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
            empty="Nothing observed connecting to this node in the window."
          />
          <DependencyList
            title="Outgoing"
            items={detail.outgoing}
            empty="This node opened no connections in the window."
          />
          {/* A Deployment fronted by a Service shows its inbound traffic on the SERVICE node,
              because that is what clients actually connect to. Without this note an empty
              "Incoming" reads as a bug rather than as the resolution rule working. */}
          {detail.incoming.length === 0 && encoding.label !== "Service" && (
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
