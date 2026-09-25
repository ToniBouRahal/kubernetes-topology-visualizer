import { expect, test } from "@playwright/test";

// Browser integration with deterministic API responses; no live cluster required.
test("failed-only connections and setup timing survive namespace grouping", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  const first_seen = "2026-09-13T12:00:00Z";
  const last_seen = "2026-09-13T12:05:00Z";
  const nodes = [
    { id: "a", name: "client", namespace: "web" },
    { id: "b", name: "database", namespace: "data" },
    { id: "c", name: "worker", namespace: "web" },
  ].map(node => ({ ...node, label: node.name, kind: "Deployment", first_seen, last_seen, attributes: {} }));
  const common = { protocol: "TCP", destination_port: 5432, first_seen, last_seen, bytes_sent: null, bytes_received: null };
  const edges = [
    { ...common, id: "failed", source_id: "a", target_id: "b", connection_count: 0, failed_connection_count: 3, connect_latency_count: 0, connect_latency_sum_us: 0 },
    { ...common, id: "success", source_id: "c", target_id: "b", connection_count: 2, failed_connection_count: 1, connect_latency_count: 2, connect_latency_sum_us: 8000 },
  ];
  const window = { start: first_seen, end: last_seen };
  await page.route("**/api/v1/**", async route => {
    const path = new URL(route.request().url()).pathname;
    const json = path.endsWith("/namespaces") ? { window, namespaces: ["web", "data"] }
      : path.includes("/nodes/") ? {
          node: nodes[0], window, incoming: [],
          outgoing: [{ ...edges[0], node_id: "b", label: "database", bytes_total: null }],
        }
      : { generated_at: last_seen, window, filters: { namespaces: [], include_external: true, include_unresolved: false }, nodes, edges,
          summary: { node_count: 3, edge_count: 2, total_connections: 2, truncated: false } };
    await route.fulfill({ json });
  });
  await page.goto("/");
  // Unselected, every edge carries its port only; the counts arrive with a selection (ADR-010 D-10.6).
  await expect(page.locator(".react-flow__edge-text")).toHaveText(["TCP:5432", "TCP:5432"]);
  // A failed-only edge is still drawn, dashed, rather than hidden for having no successes.
  await expect(page.locator('[data-id="failed"] .react-flow__edge-path')).toHaveCSS("stroke-dasharray", "6px, 4px");

  await page.locator('.react-flow__node[data-id="a"]').click();
  await expect(page.locator(".react-flow__edge-text")).toHaveText(["TCP:5432 · 0 successful · 3 failed/aborted"]);
  await expect(page.getByRole("complementary", { name: "Details for client" })).toContainText("0 successful");
  await expect(page.getByRole("complementary", { name: "Details for client" })).toContainText("3 failed/aborted");

  await page.locator('.react-flow__node[data-id="c"]').click();
  await expect(page.locator(".react-flow__edge-text")).toHaveText([
    "TCP:5432 · 2 successful · 1 failed/aborted · mean TCP setup 4 ms",
  ]);

  // Grouping merges both edges into one namespace edge; how its counts sum is grouping.test.ts.
  await page.locator(".react-flow__pane").click();
  await page.getByRole("button", { name: "Namespaces", exact: true }).click();
  await expect(page.locator(".react-flow__node")).toHaveCount(2);
  await expect(page.locator(".react-flow__edge-text")).toHaveText(["TCP:5432"]);
  expect(errors).toEqual([]);
});
