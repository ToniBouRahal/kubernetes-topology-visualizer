import { BaseEdge, type EdgeProps } from "@xyflow/react";

import { floatingPath, type Disc } from "./edgeGeometry";

/**
 * An edge drawn between the two circles along the line joining their centres.
 *
 * React Flow's default edge runs from a fixed source handle (right) to a fixed target handle
 * (left). That holds only while every target sits to the right of its source, and nothing keeps it
 * so: the layout moves a node out of another's way (layout.ts `separate`), and anyone can drag a
 * node anywhere. Once a target is behind its source, a fixed-handle edge loops backwards and
 * reads as broken. Leaving each circle on the side facing the other is right in every geometry.
 *
 * `data.bend` curves the edge to the left of its direction of travel, by that many pixels at the
 * midpoint. Left of travel means A→B and B→A bow apart instead of drawing over each other.
 *
 * `data.from` and `data.to` are the two circles, from the layout. Nodes are not draggable, so the
 * layout's positions are where they are drawn, and passing them in keeps each edge off React
 * Flow's store entirely. Reading them from the store instead cost ~35 ms per click at 2,000 edges
 * (bench/); if nodes ever become draggable, that is the trade to revisit.
 */
export function FloatingEdge({ id, markerEnd, style, label, labelStyle, labelBgStyle, labelBgPadding, labelBgBorderRadius, data }: EdgeProps) {
  const from = data?.from as Disc | undefined;
  const to = data?.to as Disc | undefined;
  if (!from || !to) return null;

  const geometry = floatingPath(from, to, Number(data?.bend ?? 0));
  // Circles touching or overlapping have no gap to draw in.
  if (!geometry) return null;
  const { path, labelX, labelY } = geometry;

  return (
    <BaseEdge
      id={id}
      path={path}
      markerEnd={markerEnd}
      style={style}
      label={label}
      labelX={labelX}
      labelY={labelY}
      labelStyle={labelStyle}
      labelBgStyle={labelBgStyle}
      labelBgPadding={labelBgPadding}
      labelBgBorderRadius={labelBgBorderRadius}
    />
  );
}
