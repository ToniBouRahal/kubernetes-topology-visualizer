import type { NodeKind } from "../../api/types";

/**
 * Visual encoding rules.
 *
 * Kind is not drawn (ADR-010 D-10.1). It was the strongest-cued fact in ADR-006 D-6.3 because a
 * Service and the workload behind it were once two different nodes; ADR-009 collapsed that
 * distinction, so kind became the Kubernetes object that happens to run a component rather than
 * anything the reader needs to answer "what depends on what". `NodeKind` is still on the wire and
 * in the database — this file decides what the CANVAS shows, not what the system records.
 *
 * What colour carries is NAMESPACE, and only namespace. It is never the sole carrier: every node
 * writes its namespace underneath itself (D-10.4), which is what keeps D-6.3's guarantee alive now
 * that kind no longer occupies that line of text.
 *
 * `External` survives as the one kind-derived fact, because it is not a kind — it is the cluster
 * boundary. Everything outside collapses into one node and no remote address is ever stored, and
 * that claim earns a mark of its own.
 */

export const NAMESPACE_HUES = [
  "var(--ns-1)",
  "var(--ns-2)",
  "var(--ns-3)",
  "var(--ns-4)",
  "var(--ns-5)",
  "var(--ns-6)",
] as const;

/**
 * Assign a namespace its colour deterministically from its name.
 *
 * Deterministic, not index-based: an index would reassign every colour the moment a new
 * namespace appeared or a filter changed, so `demo` would silently change hue between polls.
 */
export function namespaceHue(namespace: string | null | undefined): string {
  if (!namespace) return "var(--external)";
  let hash = 0;
  for (let i = 0; i < namespace.length; i += 1) {
    hash = (hash * 31 + namespace.charCodeAt(i)) >>> 0;
  }
  return NAMESPACE_HUES[hash % NAMESPACE_HUES.length]!;
}

/** The cluster boundary, drawn as a dashed outline. Not a kind cue — see the file comment. */
export function isExternal(kind: NodeKind | string): boolean {
  return kind === "External";
}

/**
 * What a node writes under itself where its namespace goes.
 *
 * The EXTERNAL node has no namespace because it is not in the cluster, and an empty line there
 * would read as missing data rather than as the boundary it actually is.
 */
export function namespaceLabel(node: { namespace?: string | null; kind: NodeKind | string }): string {
  if (isExternal(node.kind)) return "outside cluster";
  return node.namespace ?? "no namespace";
}
