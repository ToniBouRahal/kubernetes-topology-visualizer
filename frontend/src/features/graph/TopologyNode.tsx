import { Handle, Position } from "@xyflow/react";
import { memo } from "react";

import type { GraphNode } from "../../api/types";
import { NODE_HEIGHT, NODE_WIDTH } from "./layout";
import { encodingFor, namespaceHue, shapePath } from "./encoding";

export interface TopologyNodeData extends Record<string, unknown> {
  node: GraphNode;
  selected?: boolean;
}

/**
 * One graph node.
 *
 * Every node is the same outline; the kind is written on the node and the colour carries the
 * namespace. That keeps the node fully readable in greyscale — the kind is still there in words,
 * and only the namespace grouping is lost. Rendering the outline as an SVG stroke keeps it
 * legible at low zoom, where a fill would be too small to read.
 */
function TopologyNodeComponent({ data }: { data: TopologyNodeData }) {
  const { node, selected } = data;
  const encoding = encodingFor(node.kind);
  const hue = namespaceHue(node.namespace);
  const isExternal = encoding.dashed === true;

  return (
    <div
      className="topology-node"
      style={{ width: NODE_WIDTH, height: NODE_HEIGHT }}
      // The accessible name states kind and namespace in words, because a screen reader cannot
      // see either the shape or the colour.
      aria-label={`${encoding.label} ${node.name}${node.namespace ? ` in namespace ${node.namespace}` : ""}`}
    >
      <Handle type="target" position={Position.Left} className="topology-handle" />

      <svg
        width={NODE_WIDTH}
        height={NODE_HEIGHT}
        viewBox={`0 0 ${NODE_WIDTH} ${NODE_HEIGHT}`}
        className="topology-node__shape"
        aria-hidden="true"
      >
        <path
          d={shapePath(encoding.shape, NODE_WIDTH, NODE_HEIGHT)}
          fill="var(--panel)"
          stroke={hue}
          strokeWidth={selected ? 2.5 : 1.5}
          strokeDasharray={isExternal ? "5 4" : undefined}
        />
      </svg>

      <div className="topology-node__content">
        <div className="topology-node__name" title={node.label}>
          {node.label}
        </div>
        <div className="topology-node__meta">
          {/* The kind is always spelled out — it is now the only cue that carries it. */}
          <span className="topology-node__kind" style={{ color: hue }}>
            {encoding.label}
          </span>
          {node.namespace && <span className="topology-node__ns mono">{node.namespace}</span>}
        </div>
      </div>

      <Handle type="source" position={Position.Right} className="topology-handle" />
    </div>
  );
}

export const TopologyNode = memo(TopologyNodeComponent);
