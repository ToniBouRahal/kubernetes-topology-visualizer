import type { GraphNode } from "../../api/types";
import { namespaceHue, namespaceLabel } from "./encoding";

/**
 * A keyboard-navigable list of the graph's nodes.
 *
 * ADR-006 D-6.7 requires an accessible equivalent to clicking a node. A React Flow canvas cannot
 * provide one: its nodes are absolutely-positioned divs in visual order, not reading order, and
 * selection happens through pointer events.
 *
 * This is deliberately VISIBLE rather than a screen-reader-only affordance. A searchable,
 * ordered list of what is on the canvas is useful to everyone — and a hidden accessibility path
 * is one nobody tests, so it rots.
 */
export function NodeList({
  nodes,
  selectedId,
  onSelect,
}: {
  nodes: GraphNode[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  if (nodes.length === 0) return null;

  // Sorted by namespace then name: the canvas is laid out by dependency, which is the wrong
  // order to read a list in.
  const ordered = [...nodes].sort((a, b) =>
    `${a.namespace ?? ""}/${a.name}`.localeCompare(`${b.namespace ?? ""}/${b.name}`),
  );

  return (
    <section className="panel__section">
      <span className="label" id="node-list-label">
        Components <span className="mono">({nodes.length})</span>
      </span>
      <ul className="node-list" aria-labelledby="node-list-label">
        {ordered.map((node) => {
          const selected = node.id === selectedId;
          return (
            <li key={node.id}>
              <button
                type="button"
                className={`node-list__item ${selected ? "node-list__item--on" : ""}`}
                aria-pressed={selected}
                onClick={() => onSelect(node.id)}
              >
                <span
                  className="node-list__chip"
                  style={{ background: namespaceHue(node.namespace) }}
                  aria-hidden="true"
                />
                <span className="node-list__name">{node.label}</span>
                {/* Namespace in words. Kind is not shown anywhere any more (ADR-010 D-10.1), and
                    the colour chip beside the name needs a written counterpart here as much as
                    the canvas does. */}
                <span className="node-list__ns mono">{namespaceLabel(node)}</span>
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
