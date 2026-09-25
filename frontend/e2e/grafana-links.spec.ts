import { expect, test, type Page } from "@playwright/test";

/**
 * Grafana deep links from the details panel (ADR-012, ADR-013).
 *
 * The first group mocks the API and /config.json, so it needs no cluster. The last test follows a
 * real link into the bundled Grafana and runs only when that is reachable:
 *   make demo-observability
 *   kubectl --context kind-topology -n topology port-forward svc/topology-grafana 3000:80
 *   GRAFANA_PASSWORD=$(kubectl --context kind-topology -n topology get secret topology-grafana \
 *     -o jsonpath='{.data.admin-password}' | base64 -d) npx playwright test grafana-links
 */

const GRAFANA = "http://localhost:3000";
const first_seen = "2026-09-13T12:00:00Z";
const last_seen = "2026-09-13T12:05:00Z";
const window = { start: first_seen, end: last_seen };

const nodes = [
  { id: "d", kind: "Deployment", name: "backend", namespace: "demo" },
  { id: "s", kind: "StatefulSet", name: "redis", namespace: "data" },
  { id: "svc", kind: "Service", name: "cache", namespace: "data" },
].map((node) => ({ ...node, label: node.name, first_seen, last_seen, attributes: {} }));

const common = {
  protocol: "TCP", first_seen, last_seen, bytes_sent: null, bytes_received: null,
  failed_connection_count: 0, connect_latency_count: 0, connect_latency_sum_us: 0,
};
const edges = [
  { ...common, id: "e1", source_id: "d", target_id: "s", destination_port: 6379, connection_count: 5 },
  { ...common, id: "e2", source_id: "d", target_id: "svc", destination_port: 11211, connection_count: 2 },
];

async function mockStack(page: Page, config: unknown) {
  await page.route("**/config.json", (route) => route.fulfill({ json: config }));
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const nodeId = path.match(/\/nodes\/([^/]+)/)?.[1];
    const json = path.endsWith("/namespaces")
      ? { window, namespaces: ["data", "demo"] }
      : nodeId
        ? { node: nodes.find((n) => n.id === decodeURIComponent(nodeId)), window, incoming: [], outgoing: [] }
        : {
            generated_at: last_seen, window, nodes, edges,
            filters: { namespaces: [], include_external: true, include_unresolved: false },
            summary: { node_count: 3, edge_count: 2, total_connections: 7, truncated: false },
          };
    await route.fulfill({ json });
  });
}

async function select(page: Page, id: string, label: string) {
  await page.goto("/");
  await page.locator(`.react-flow__node[data-id="${id}"]`).click();
  const details = page.getByRole("complementary", { name: `Details for ${label}` });
  await expect(details).toBeVisible();
  return details;
}

test.describe("Grafana links, mocked API", () => {
  test("a workload links to its dashboard with namespace, kind, name and the window", async ({ page }) => {
    await mockStack(page, { grafana: { url: `${GRAFANA}/`, workloadDashboardUid: "topology-workload", lokiDatasourceUid: "" } });
    const details = await select(page, "s", "redis");

    const metrics = details.getByRole("link", { name: /Metrics/ });
    await expect(metrics).toHaveAttribute("target", "_blank");
    await expect(metrics).toHaveAttribute("rel", /noopener/);
    const url = new URL((await metrics.getAttribute("href"))!);
    expect(`${url.origin}${url.pathname}`).toBe(`${GRAFANA}/d/topology-workload`); // trailing slash trimmed
    expect(Object.fromEntries(url.searchParams)).toEqual({
      "var-namespace": "data",
      "var-type": "statefulset",
      "var-workload": "redis",
      from: String(Date.parse(first_seen)),
      to: String(Date.parse(last_seen)),
    });
    // No Loki datasource configured: no Logs button, not a disabled one (D-12.1).
    await expect(details.getByRole("link", { name: /Logs/ })).toHaveCount(0);
  });

  test("a Loki datasource adds a Logs link into Explore", async ({ page }) => {
    await mockStack(page, { grafana: { url: GRAFANA, workloadDashboardUid: "topology-workload", lokiDatasourceUid: "loki" } });
    const details = await select(page, "d", "backend");
    const href = (await details.getByRole("link", { name: /Logs/ }).getAttribute("href"))!;
    const panes = JSON.parse(new URL(href).searchParams.get("panes")!);
    expect(panes.topology.queries[0].expr).toBe('{namespace="demo", pod=~"backend-.*"}');
  });

  test("a Service has no Grafana section", async ({ page }) => {
    await mockStack(page, { grafana: { url: GRAFANA, workloadDashboardUid: "topology-workload", lokiDatasourceUid: "loki" } });
    const details = await select(page, "svc", "cache");
    await expect(details.getByText("In Grafana")).toHaveCount(0);
  });

  test("without Grafana configured the section is absent", async ({ page }) => {
    await mockStack(page, {});
    const details = await select(page, "d", "backend");
    await expect(details.getByText("In Grafana")).toHaveCount(0);
  });
});

test("the Metrics link opens a populated dashboard in the bundled Grafana", async ({ page, context }) => {
  const password = process.env.GRAFANA_PASSWORD;
  test.skip(!password, "GRAFANA_PASSWORD unset — needs `make demo-observability` and a port-forward");

  await page.goto("/");
  const node = page
    .locator(".react-flow__node")
    .filter({ has: page.locator(".topology-node__name", { hasText: /^redis$/ }) })
    .first();
  await expect(node).toBeVisible({ timeout: 30_000 });
  await node.click();
  const metrics = page.getByLabel(/^Details for/).getByRole("link", { name: /Metrics/ });
  await expect(metrics).toBeVisible();

  // Grafana redirects an unauthenticated page load to /login rather than challenging, so
  // httpCredentials would never be sent; a header is.
  await context.setExtraHTTPHeaders({
    Authorization: `Basic ${Buffer.from(`admin:${password}`).toString("base64")}`,
  });
  const [tab] = await Promise.all([context.waitForEvent("page"), metrics.click()]);
  await expect(tab).toHaveTitle(/Topology Visualizer — workload/, { timeout: 30_000 });
  await expect(tab.getByText("Pods ready").first()).toBeVisible({ timeout: 30_000 });
  await expect(tab.getByText("No data")).toHaveCount(0, { timeout: 15_000 });
});
