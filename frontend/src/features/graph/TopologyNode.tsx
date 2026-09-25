import { Handle, Position } from "@xyflow/react";
import { memo } from "react";

import type { GraphNode } from "../../api/types";
import { NODE_LABEL_BAND, nodeDiameter } from "./layout";
import { isExternal, namespaceHue, namespaceLabel } from "./encoding";

export interface TopologyNodeData extends Record<string, unknown> {
  node: GraphNode;
  selected?: boolean;
  group?: { namespace: string; workloads: number };
  /** Distinct components this one talks to. Drives the diameter — ADR-010 D-10.3. */
  degree?: number;
}

/**
 * One graph node: a circle with its own name inside it.
 *
 * The name is the thing a reader searches for, points at, and says out loud, so it gets the node
 * itself rather than a caption beside one (ADR-010 D-10.2). Kind is not drawn at all (D-10.1).
 * Namespace is written in the band beneath, which is what stops colour being its only carrier
 * (D-10.4) and keeps the node readable in greyscale.
 *
 * Selection inverts the fill rather than adding a colour: the graph's whole hue budget belongs to
 * namespace, and a highlight colour would have to be stolen from it.
 */
function TopologyNodeComponent({ data }: { data: TopologyNodeData }) {
  const { node, selected, group, degree = 0 } = data;
  const hue = group ? namespaceHue(group.namespace) : namespaceHue(node.namespace);
  const external = isExternal(node.kind);
  const diameter = nodeDiameter(degree);

  const label = group ? group.namespace : node.label;
  const band = group ? `${group.workloads} workloads · expand` : namespaceLabel(node);

  // The circle grows with degree, not with the length of the name, so a long name steps the type
  // down instead of fragmenting across lines. The thresholds are what fits the MINIMUM diameter —
  // a name that fits the smallest circle fits every larger one.
  const fit = label.length > 13 ? " topology-node__name--longer" : label.length > 7 ? " topology-node__name--long" : "";

  return (
    <div
      className={`topology-node${selected ? " topology-node--selected" : ""}`}
      style={{ width: diameter, height: diameter + NODE_LABEL_BAND }}
      // The accessible name carries what the circle carries, in words: a screen reader sees
      // neither the fill nor the diameter. Degree is stated because it is now an encoding.
      aria-label={
        group
          ? `Namespace ${group.namespace}, ${group.workloads} workloads, talks to ${degree} components`
          : `${node.name} in ${namespaceLabel(node)}, talks to ${degree} components`
      }
    >
      <Handle type="target" position={Position.Left} className="topology-handle" />

      <div
        className={`topology-node__disc${external ? " topology-node__disc--external" : ""}`}
        style={{
          width: diameter,
          height: diameter,
          background: selected ? "var(--panel-high)" : hue,
          borderColor: hue,
          color: selected ? hue : "var(--on-fill)",
        }}
      >
        <span className={`topology-node__name${fit}`} title={label}>
          {label}
        </span>
      </div>

      <div className="topology-node__ns mono" title={band}>
        {band}
      </div>

      <Handle type="source" position={Position.Right} className="topology-handle" />
    </div>
  );
}

export const TopologyNode = memo(TopologyNodeComponent);
