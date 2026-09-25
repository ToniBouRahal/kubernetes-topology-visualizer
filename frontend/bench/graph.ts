/**
 * A synthetic graph of a stated size, shaped like scripts/seed-scale.py produces.
 *
 * The benchmark serves this from mocked API routes instead of a cluster, so a run measures the
 * browser and nothing else. Seeded, so every run draws the same graph and runs compare.
 */

function prng(seed: number) {
  let state = seed >>> 0;
  return () => {
    // mulberry32
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const KINDS = ["Deployment", "StatefulSet", "DaemonSet", "Job", "Pod"] as const;
const CLUSTER = "kind-topology";

export function syntheticGraph(nodeCount: number, edgeCount: number) {
  const rnd = prng(20260822);
  const now = new Date();
  now.setUTCSeconds(0, 0);
  const lastSeen = now.toISOString();
  const firstSeen = new Date(now.getTime() - 10_000).toISOString();
  const start = new Date(now.getTime() - 5 * 60_000).toISOString();

  const node = (ns: string, kind: string, name: string) => ({
    id: `k8s:${CLUSTER}:${ns}:${kind}:${name}`,
    kind,
    namespace: ns,
    name,
    label: name,
    attributes: {},
    first_seen: firstSeen,
    last_seen: lastSeen,
  });

  // Sources are workloads, targets Services — a source never resolves to a Service.
  const workloads = Array.from({ length: Math.floor(nodeCount / 2) }, (_, i) =>
    node(`scale-ns${i % 20}`, KINDS[i % KINDS.length]!, `wl-${i}`),
  );
  const services = Array.from({ length: nodeCount - workloads.length }, (_, i) =>
    node(`scale-ns${i % 20}`, "Service", `svc-${i}`),
  );

  const seen = new Set<string>();
  const edges = [];
  const ports = [80, 443, 5432, 6379, 8080, 9090];
  while (edges.length < edgeCount) {
    const src = workloads[Math.floor(rnd() * workloads.length)]!;
    const dst = services[Math.floor(rnd() * services.length)]!;
    const port = ports[Math.floor(rnd() * ports.length)];
    const id = `${src.id}|${dst.id}|TCP|${port}`;
    if (seen.has(id)) continue;
    seen.add(id);
    const count = Math.max(1, Math.floor(1000 * rnd() ** 3));
    const failed = rnd() < 0.05 ? Math.ceil(rnd() * 10) : 0;
    edges.push({
      id,
      source_id: src.id,
      target_id: dst.id,
      protocol: "TCP",
      destination_port: port,
      connection_count: count,
      failed_connection_count: failed,
      connect_latency_count: count,
      connect_latency_sum_us: count * Math.floor(200 + rnd() * 2000),
      first_seen: firstSeen,
      last_seen: lastSeen,
    });
  }

  // Only nodes an edge references, as the API returns them (ADR-004 D-4.4).
  const referenced = new Set(edges.flatMap((e) => [e.source_id, e.target_id]));
  const nodes = [...workloads, ...services].filter((n) => referenced.has(n.id));

  return {
    nodes,
    edges,
    filters: { include_external: true, include_unresolved: false, namespaces: [] },
    generated_at: lastSeen,
    summary: {
      node_count: nodes.length,
      edge_count: edges.length,
      total_connections: edges.reduce((sum, e) => sum + e.connection_count, 0),
      truncated: false,
      truncation_reason: null,
    },
    window: { start, end: lastSeen },
  };
}
