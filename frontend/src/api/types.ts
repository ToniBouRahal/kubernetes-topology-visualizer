/**
 * Convenience aliases over the generated schema.
 *
 * Every shape here comes from `contracts/openapi.json` via `npm run generate:api`. Nothing in
 * this file re-declares a payload type by hand — ADR-006 D-6.1 forbids it, because a
 * hand-written duplicate silently stops matching the backend the moment the contract moves.
 */
import type { components } from "./generated/schema";

type Schemas = components["schemas"];

export type GraphResponse = Schemas["GraphResponse"];
export type GraphNode = Schemas["GraphNode"];
export type GraphEdge = Schemas["GraphEdge"];
export type GraphSummary = Schemas["GraphSummary"];
export type EffectiveFilters = Schemas["EffectiveFilters"];
export type TimeWindow = Schemas["TimeWindow"];
export type NodeDetail = Schemas["NodeDetail"];
export type EdgeDetail = Schemas["EdgeDetail"];
export type NodeDependency = Schemas["NodeDependency"];
export type NamespaceList = Schemas["NamespaceList"];
export type ErrorResponse = Schemas["ErrorResponse"];

/** The six workload kinds plus the external node's presentation kind. */
export type NodeKind = GraphNode["kind"];

export const EXTERNAL_NODE_ID = "external:EXTERNAL";

/** Window presets, fixed by ADR-001 §5.3. */
export const WINDOW_PRESETS = ["1m", "5m", "15m", "1h", "6h", "24h"] as const;
export type WindowPreset = (typeof WINDOW_PRESETS)[number];

export interface GraphQuery {
  window?: WindowPreset;
  from?: string;
  to?: string;
  namespace?: string[];
  kind?: string;
  query?: string;
  includeExternal?: boolean;
  includeUnresolved?: boolean;
}
