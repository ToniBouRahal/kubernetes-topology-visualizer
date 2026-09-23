/**
 * Grafana deep links for a selected node (ADR-012 D-12.3, D-12.4).
 *
 * Built from the node's FIELDS — namespace, kind, name — never from its id, which is opaque
 * (`contracts/ids.md` §2). The links point at the cluster's own telemetry about the workload, not
 * at anything this tool collected; the panel says so next to them.
 */
import type { GraphNode, TimeWindow } from "../../api/types";
import type { GrafanaConfig } from "../../config";

export interface GrafanaLinks {
  metrics?: string;
  logs?: string;
}

/** Kinds the workload dashboard has a row for, and whose pods are named after them. */
const WORKLOAD_KINDS = new Set<GraphNode["kind"]>(["Deployment", "StatefulSet", "DaemonSet", "Job"]);

function lokiString(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

/** So `api.v2` matches a literal dot rather than any character (D-12.6). */
function regexLiteral(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Grafana takes epoch milliseconds or relative expressions in the same parameters. The window the
 * counts were computed over is the honest choice; before it has loaded, the last hour.
 */
function range(window: TimeWindow | null | undefined): { from: string; to: string } {
  if (!window) return { from: "now-1h", to: "now" };
  const from = Date.parse(window.start);
  const to = Date.parse(window.end);
  if (Number.isNaN(from) || Number.isNaN(to)) return { from: "now-1h", to: "now" };
  return { from: String(from), to: String(to) };
}

export function grafanaLinks(
  node: GraphNode,
  grafana: GrafanaConfig | null | undefined,
  window?: TimeWindow | null,
): GrafanaLinks {
  if (!grafana || !node.namespace) return {};

  const isWorkload = WORKLOAD_KINDS.has(node.kind);
  const isPod = node.kind === "Pod";
  // A Service here is ADR-009's fallback — zero or several workloads behind it — and External is
  // not in the cluster. Neither has a page that would be correct.
  if (!isWorkload && !isPod) return {};

  const { from, to } = range(window);
  const links: GrafanaLinks = {};

  if (isWorkload && grafana.workloadDashboardUid) {
    const params = new URLSearchParams({
      "var-namespace": node.namespace,
      "var-type": node.kind.toLowerCase(),
      "var-workload": node.name,
      from,
      to,
    });
    links.metrics = `${grafana.url}/d/${encodeURIComponent(grafana.workloadDashboardUid)}?${params}`;
  }

  if (grafana.lokiDatasourceUid) {
    const pod = isPod
      ? `pod="${lokiString(node.name)}"`
      : `pod=~"${lokiString(regexLiteral(node.name))}-.*"`;
    const expr = `{namespace="${lokiString(node.namespace)}", ${pod}}`;
    const uid = grafana.lokiDatasourceUid;
    // Grafana 10+ Explore URL: one pane, one query, the same range as the panel.
    const panes = {
      topology: {
        datasource: uid,
        queries: [{ refId: "A", datasource: { type: "loki", uid }, expr }],
        range: { from, to },
      },
    };
    const params = new URLSearchParams({ schemaVersion: "1", panes: JSON.stringify(panes) });
    links.logs = `${grafana.url}/explore?${params}`;
  }

  return links;
}
