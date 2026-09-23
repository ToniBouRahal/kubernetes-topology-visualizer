/**
 * Grafana deep links — ADR-012 T-12.1 – T-12.3.
 *
 * The links are built from the node's fields and the panel's window. What matters is that the
 * right nodes get the right links, that a name cannot break out of the query it is placed in, and
 * that a node with nothing correct to link to gets nothing rather than a guess.
 */
import { describe, expect, it } from "vitest";
import type { GraphNode, TimeWindow } from "../src/api/types";
import type { GrafanaConfig } from "../src/config";
import { grafanaLinks } from "../src/features/details/grafanaLinks";

const grafana: GrafanaConfig = {
  url: "https://grafana.example.com",
  workloadDashboardUid: "wl-dash",
  lokiDatasourceUid: "loki-1",
};

function node(kind: GraphNode["kind"], name = "backend", namespace: string | null = "demo"): GraphNode {
  return {
    id: "opaque",
    kind,
    name,
    label: name,
    namespace,
    first_seen: "2026-01-01T00:00:00Z",
    last_seen: "2026-01-01T01:00:00Z",
    attributes: {},
  };
}

const window: TimeWindow = { start: "2026-01-01T00:00:00Z", end: "2026-01-01T00:05:00Z" };

/** The Explore URL carries its query as JSON in a `panes` parameter. */
function explorePane(url: string): { datasource: string; expr: string; range: { from: string; to: string } } {
  const panes = new URL(url).searchParams.get("panes")!;
  const pane = JSON.parse(panes).topology;
  return { datasource: pane.datasource, expr: pane.queries[0].expr, range: pane.range };
}

describe("nothing to link to — T-12.1", () => {
  it("yields no links without a config", () => {
    expect(grafanaLinks(node("Deployment"), null, window)).toEqual({});
    expect(grafanaLinks(node("Deployment"), undefined, window)).toEqual({});
  });

  it("yields no links for a node outside any namespace", () => {
    expect(grafanaLinks(node("External", "EXTERNAL", null), grafana, window)).toEqual({});
  });

  it("yields nothing for a Service or External node even when configured", () => {
    // A Service node is ADR-009's fallback — zero or several workloads behind it — and linking to
    // one of them would invent a fact the graph does not have.
    expect(grafanaLinks(node("Service"), grafana, window)).toEqual({});
    expect(grafanaLinks(node("External", "EXTERNAL", "demo"), grafana, window)).toEqual({});
  });

  it("drops each button when its own UID is missing", () => {
    expect(grafanaLinks(node("Deployment"), { ...grafana, workloadDashboardUid: "" }, window).metrics).toBeUndefined();
    expect(grafanaLinks(node("Deployment"), { ...grafana, lokiDatasourceUid: "" }, window).logs).toBeUndefined();
  });
});

describe("which node gets which link — T-12.2", () => {
  it("gives a workload a dashboard link with namespace, lower-cased type, name and the window", () => {
    const { metrics } = grafanaLinks(node("StatefulSet", "redis", "data"), grafana, window);
    const url = new URL(metrics!);
    expect(url.origin + url.pathname).toBe("https://grafana.example.com/d/wl-dash");
    expect(url.searchParams.get("var-namespace")).toBe("data");
    expect(url.searchParams.get("var-type")).toBe("statefulset");
    expect(url.searchParams.get("var-workload")).toBe("redis");
    expect(url.searchParams.get("from")).toBe(String(Date.parse(window.start)));
    expect(url.searchParams.get("to")).toBe(String(Date.parse(window.end)));
  });

  it("gives a workload a logs link on pods named after it, over the same window", () => {
    const { logs } = grafanaLinks(node("Deployment"), grafana, window);
    const pane = explorePane(logs!);
    expect(pane.datasource).toBe("loki-1");
    expect(pane.expr).toBe('{namespace="demo", pod=~"backend-.*"}');
    expect(pane.range).toEqual({ from: String(Date.parse(window.start)), to: String(Date.parse(window.end)) });
    expect(new URL(logs!).pathname).toBe("/explore");
  });

  it("gives a standalone Pod logs only, matched exactly", () => {
    const links = grafanaLinks(node("Pod", "debug-shell"), grafana, window);
    expect(links.metrics).toBeUndefined();
    expect(explorePane(links.logs!).expr).toBe('{namespace="demo", pod="debug-shell"}');
  });

  it("falls back to the last hour before the detail has loaded", () => {
    const { metrics } = grafanaLinks(node("Deployment"), grafana, null);
    expect(new URL(metrics!).searchParams.get("from")).toBe("now-1h");
  });
});

describe("names cannot escape the query — T-12.3", () => {
  it("escapes regex metacharacters so a dot matches a dot", () => {
    const { logs } = grafanaLinks(node("Deployment", "api.v2"), grafana, window);
    // Two layers: the regex needs `\.`, and a LogQL string literal is Go-escaped, so the
    // backslash itself is written `\\`. The selector Loki parses is the regex `api\.v2-.*`.
    expect(explorePane(logs!).expr).toBe('{namespace="demo", pod=~"api\\\\.v2-.*"}');
  });

  it("escapes a quote so it cannot close the selector", () => {
    const { logs } = grafanaLinks(node("Pod", 'x"y'), grafana, window);
    expect(explorePane(logs!).expr).toBe('{namespace="demo", pod="x\\"y"}');
  });

  it("does not double a trailing slash on the base URL", () => {
    // parseUiConfig strips it, but the builder must not depend on that.
    const { metrics } = grafanaLinks(node("Deployment"), { ...grafana, url: "https://g.example.com/grafana" }, window);
    expect(metrics!.startsWith("https://g.example.com/grafana/d/")).toBe(true);
  });
});
