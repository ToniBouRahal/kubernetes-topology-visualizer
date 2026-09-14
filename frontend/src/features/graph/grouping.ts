import type { GraphEdge, GraphNode } from "../../api/types";

export interface NamespaceGroup { namespace: string; workloads: number }

/** View-only identities; never sent to the API or inferred from workload IDs. */
export function groupTopology(nodes: GraphNode[], edges: GraphEdge[], expanded: Set<string>) {
  const groups = new Map<string, NamespaceGroup>();
  const representatives = new Map<string, string>();
  const groupedNodes = new Map<string, GraphNode>();
  const reserved = new Set(nodes.map(n => n.id));
  const namespaceIds = new Map<string, string>();
  for (const ns of [...new Set(nodes.filter(n => n.kind !== "External" && n.namespace !== null).map(n => n.namespace!))].sort()) {
    let id = `namespace:${JSON.stringify(ns)}`;
    while (reserved.has(id)) id = `_${id}`;
    reserved.add(id);
    namespaceIds.set(ns, id);
  }
  for (const node of nodes) {
    if (node.kind === "External" || node.namespace === null || expanded.has(node.namespace)) {
      representatives.set(node.id, node.id);
      groupedNodes.set(node.id, node);
      continue;
    }
    const id = namespaceIds.get(node.namespace)!;
    representatives.set(node.id, id);
    const group = groups.get(id);
    if (group) group.workloads++;
    else {
      groups.set(id, { namespace: node.namespace, workloads: 1 });
      // Reuse the layout's node shape; presentation is supplied separately via groups.
      groupedNodes.set(id, { ...node, id, name: node.namespace, label: node.namespace });
    }
  }
  const groupedEdges = new Map<string, GraphEdge>();
  for (const edge of edges) {
    const source_id = representatives.get(edge.source_id);
    const target_id = representatives.get(edge.target_id);
    if (!source_id || !target_id) continue;
    const id = JSON.stringify([source_id, target_id, edge.protocol, edge.destination_port]);
    const previous = groupedEdges.get(id);
    if (previous) {
      previous.connection_count += edge.connection_count;
      previous.failed_connection_count = previous.failed_connection_count == null && edge.failed_connection_count == null
        ? null : (previous.failed_connection_count ?? 0) + (edge.failed_connection_count ?? 0);
      previous.connect_latency_count = (previous.connect_latency_count ?? 0) + (edge.connect_latency_count ?? 0);
      previous.connect_latency_sum_us = (previous.connect_latency_sum_us ?? 0) + (edge.connect_latency_sum_us ?? 0);
      previous.first_seen = previous.first_seen < edge.first_seen ? previous.first_seen : edge.first_seen;
      previous.last_seen = previous.last_seen > edge.last_seen ? previous.last_seen : edge.last_seen;
    } else groupedEdges.set(id, { ...edge, id, source_id, target_id, failed_connection_count: edge.failed_connection_count ?? null, connect_latency_count: edge.connect_latency_count ?? 0, connect_latency_sum_us: edge.connect_latency_sum_us ?? 0, bytes_sent: null, bytes_received: null });
  }
  return { nodes: [...groupedNodes.values()], edges: [...groupedEdges.values()], groups };
}

/** Both incoming and outgoing direct relationships, before any rendering cap. */
export function focusTopology(nodes: GraphNode[], edges: GraphEdge[], selectedId: string) {
  const incident = edges.filter(e => e.source_id === selectedId || e.target_id === selectedId);
  const ids = new Set([selectedId, ...incident.flatMap(e => [e.source_id, e.target_id])]);
  return { nodes: nodes.filter(n => ids.has(n.id)), edges: incident };
}
