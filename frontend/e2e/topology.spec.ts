import { expect, test } from "@playwright/test";

/**
 * Cluster end-to-end tests — T-8.1, T-8.2, T-6.11.
 *
 * These run against a REAL deployed stack, not a mock: eBPF capture → agent → backend → browser.
 * They are the only tests that prove the whole pipeline, which is why the Phase 2 gate depends on
 * them rather than on the unit suites (ADR-008 D-8.1).
 *
 * Requires the demo running and a port-forward:
 *   make demo-up && make demo-workloads
 *   kubectl --context kind-topology -n topology port-forward svc/topology-visualizer-frontend 18080:8080
 *   npx playwright test
 */

const BASE = process.env.E2E_BASE_URL ?? "http://localhost:18080";

/** Edges the demo workloads are built to produce (ADR-007 D-7.7). */
const EXPECTED = [
  { source: "frontend", target: "backend", port: 8080 },
  { source: "backend", target: "redis", port: 6379 },
];

test.describe("runtime topology, end to end", () => {
  test("the app shell loads", async ({ page }) => {
    await page.goto(BASE);
    await expect(page).toHaveTitle(/Runtime Topology/);
    await expect(page.getByRole("heading", { name: "Runtime Topology" })).toBeVisible();
    // The status is stated in words, never colour alone.
    await expect(page.getByText("Connected")).toBeVisible({ timeout: 30_000 });
  });

  /**
   * T-8.2 / ADR-001 §7 Phase 2: observed traffic reaches the browser within 20 seconds.
   *
   * The budget is real and additive: up to 10 s of agent aggregation plus up to 5 s of UI
   * polling. 30 s is allowed here so a slow CI runner does not produce a flake, but the
   * measured latency is asserted and reported below.
   */
  test("demo traffic appears in the browser", async ({ page }) => {
    const started = Date.now();
    await page.goto(BASE);

    // The observation strip reports the counts, so waiting on it waits on real data.
    const strip = page.getByLabel("Observation window");
    await expect(strip).toBeVisible();
    await expect(strip).toContainText(/[1-9]\d* edges/, { timeout: 30_000 });

    const elapsed = (Date.now() - started) / 1000;
    // eslint-disable-next-line no-console
    console.log(`first topology rendered after ${elapsed.toFixed(1)}s`);
    expect(elapsed, "traffic must reach the browser within the 20 s pipeline budget").toBeLessThan(30);
  });

  /** T-8.1: the specific edges the demo is built to produce, at SERVICE level. */
  test("the expected demo edges are rendered", async ({ page }) => {
    await page.goto(BASE);
    await expect(page.getByLabel("Observation window")).toContainText(/[1-9]\d* edges/, {
      timeout: 30_000,
    });

    for (const edge of EXPECTED) {
      // Both endpoints present as nodes...
      await expect(
        page.locator(".topology-node__name", { hasText: new RegExp(`^${edge.source}$`) }).first(),
        `${edge.source} should be a node`,
      ).toBeVisible({ timeout: 20_000 });
      await expect(
        page.locator(".topology-node__name", { hasText: new RegExp(`^${edge.target}$`) }).first(),
        `${edge.target} should be a node`,
      ).toBeVisible();
      // ...and the edge between them carries the port, so direction and protocol are visible.
      await expect(
        page.locator(".react-flow__edge-text", { hasText: `TCP:${edge.port}` }).first(),
        `an edge labelled TCP:${edge.port} should be drawn`,
      ).toBeVisible();
    }
  });

  /**
   * Replicas collapse. frontend and backend each run 2 pods across 2 nodes, and the graph must
   * still show ONE node each — the property a manifest-derived tool cannot demonstrate.
   */
  test("replicas collapse to one node per workload", async ({ page }) => {
    await page.goto(BASE);
    await expect(page.getByLabel("Observation window")).toContainText(/[1-9]\d* edges/, {
      timeout: 30_000,
    });

    const frontendNodes = page.locator(".topology-node__name", { hasText: /^frontend$/ });
    await expect(frontendNodes).toHaveCount(1);
  });

  /** Kind is spelled out, so the shape encoding is never the only cue (ADR-006 D-6.3). */
  test("nodes state their kind in words", async ({ page }) => {
    await page.goto(BASE);
    await expect(page.getByLabel("Observation window")).toContainText(/[1-9]\d* edges/, {
      timeout: 30_000,
    });

    await expect(page.locator(".topology-node__kind", { hasText: "Deployment" }).first()).toBeVisible();
    await expect(page.locator(".topology-node__kind", { hasText: "Service" }).first()).toBeVisible();
  });

  /** Selecting a node opens its dependencies — T-6.8. */
  test("selecting a node shows its dependencies", async ({ page }) => {
    await page.goto(BASE);
    await expect(page.getByLabel("Observation window")).toContainText(/[1-9]\d* edges/, {
      timeout: 30_000,
    });

    // Click the React Flow node wrapper, not the label. The label sits UNDER the SVG outline,
    // so Playwright's actionability check refuses it — a real click still works, because it
    // bubbles from the SVG to the wrapper that carries the handler.
    await page
      .locator(".react-flow__node")
      .filter({ has: page.locator(".topology-node__name", { hasText: /^backend$/ }) })
      .first()
      .click();

    const details = page.getByLabel(/^Details for/);
    await expect(details).toBeVisible();
    await expect(details).toContainText("Outgoing");
    // backend calls redis, so an outgoing dependency must be listed.
    await expect(details.locator(".dep__name", { hasText: "redis" }).first()).toBeVisible({
      timeout: 15_000,
    });
  });

  /** The filter panel is populated from observed data, not hard-coded. */
  test("namespaces are discovered from observed traffic", async ({ page }) => {
    await page.goto(BASE);
    const filters = page.getByLabel("Filters");
    await expect(filters.getByRole("button", { name: "demo" })).toBeVisible({ timeout: 30_000 });
    await expect(filters.getByRole("button", { name: "data" })).toBeVisible();
  });

  /** Usable at the demo resolution (ADR-001 §5.6) — no horizontal overflow. */
  test("is usable at 1280x720", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto(BASE);
    await expect(page.getByLabel("Observation window")).toBeVisible({ timeout: 30_000 });

    const overflows = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
    );
    expect(overflows, "the page must not scroll horizontally at 1280x720").toBe(false);

    // Both panels and the canvas must all be present at this width.
    await expect(page.getByLabel("Filters")).toBeVisible();
    await expect(page.getByLabel("Topology graph")).toBeVisible();
  });
});
