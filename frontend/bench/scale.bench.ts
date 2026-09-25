import { mkdirSync, writeFileSync } from "node:fs";
import { test, type Page } from "@playwright/test";

import { syntheticGraph } from "./graph";

/**
 * How the topology canvas behaves at the sizes ADR-001 §6 names (P5-F18).
 *
 * The graph API is mocked with a seeded synthetic graph, so this measures the browser alone —
 * the API side is already measured by `scripts/experiments.sh latency`. See bench/README.md.
 */

const SIZES = (process.env.BENCH_SIZES ?? "100x300,170x1000,500x2000").split(",").map((s) => {
  const [nodes, edges] = s.split("x").map(Number);
  return { nodes, edges };
});
/** Past this, the tab is reported as hung rather than waited on. */
const HANG_MS = Number(process.env.BENCH_HANG_MS ?? 60_000);
const LABEL = process.env.BENCH_LABEL ?? "run";
const LIVE = process.env.BENCH_LIVE === "1";

interface Settled {
  /** Action to the last DOM change in the canvas, once it has stayed quiet. */
  settledMs: number;
  /** Sum and worst of main-thread tasks over 50 ms during the action. */
  longTaskMs: number;
  worstTaskMs: number;
}

/**
 * Run `action` in the page and wait until the canvas stops changing.
 *
 * Mutation timestamps are taken in the observer callback, which cannot run until the main thread
 * is free — so a render that blocks the thread is counted in full, not just its start.
 */
async function settle(page: Page, action: string, quietMs = 1_000): Promise<Settled | null> {
  const run = page.evaluate(
    ({ action, quietMs }) =>
      new Promise<Settled>((resolve) => {
        // The parent, not .react-flow itself: switching view remounts the canvas.
        const target = document.querySelector(".react-flow")?.parentElement ?? document.body;
        const tasks: number[] = [];
        const lt = new PerformanceObserver((list) => {
          for (const e of list.getEntries()) tasks.push(e.duration);
        });
        lt.observe({ type: "longtask", buffered: false });
        const t0 = performance.now();
        let last = t0;
        let timer = 0;
        const done = () => {
          mo.disconnect();
          lt.disconnect();
          resolve({
            settledMs: Math.round(last - t0),
            longTaskMs: Math.round(tasks.reduce((a, b) => a + b, 0)),
            worstTaskMs: Math.round(Math.max(0, ...tasks)),
          });
        };
        const arm = () => {
          window.clearTimeout(timer);
          timer = window.setTimeout(done, quietMs);
        };
        const mo = new MutationObserver(() => {
          last = performance.now();
          arm();
        });
        mo.observe(target, { subtree: true, childList: true, attributes: true, characterData: true });
        arm();
        // eslint-disable-next-line no-new-func
        new Function(action)();
      }),
    { action, quietMs },
  );
  const hung = new Promise<null>((r) => setTimeout(() => r(null), HANG_MS));
  return Promise.race([run, hung]);
}

/** Median of ten double-rAF round trips: how long the page takes to answer a frame. */
async function frameResponse(page: Page): Promise<number> {
  return page.evaluate(async () => {
    const samples: number[] = [];
    for (let i = 0; i < 10; i++) {
      const t0 = performance.now();
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      samples.push(performance.now() - t0);
    }
    samples.sort((a, b) => a - b);
    return Math.round(samples[5]);
  });
}

/** Long tasks while nothing is done but wait — covers two 5 s polls of an unchanged graph. */
async function idle(page: Page, ms: number) {
  return page.evaluate(
    (ms) =>
      new Promise<{ longTaskMs: number; worstTaskMs: number }>((resolve) => {
        const tasks: number[] = [];
        const lt = new PerformanceObserver((list) => {
          for (const e of list.getEntries()) tasks.push(e.duration);
        });
        lt.observe({ type: "longtask", buffered: false });
        setTimeout(() => {
          lt.disconnect();
          resolve({
            longTaskMs: Math.round(tasks.reduce((a, b) => a + b, 0)),
            worstTaskMs: Math.round(Math.max(0, ...tasks)),
          });
        }, ms);
      }),
    ms,
  );
}

const results: Record<string, unknown>[] = [];

test.describe.configure({ mode: "serial" });

for (const size of SIZES) {
  test(`${size.nodes} nodes / ${size.edges} edges`, async ({ page }) => {
    test.setTimeout(HANG_MS * 3 + 60_000);
    const graph = syntheticGraph(size.nodes, size.edges);
    const namespaces = [...new Set(graph.nodes.map((n) => n.namespace))].sort();
    const body = JSON.stringify(graph);

    // BENCH_LIVE=1: every poll carries new counts, as a live cluster's does. Otherwise identical.
    let poll = 0;
    await page.route("**/api/v1/graph**", (r) => {
      if (!LIVE) return r.fulfill({ contentType: "application/json", body });
      poll += 1;
      const edges = graph.edges.map((e, i) => ({ ...e, connection_count: e.connection_count + ((i * 7 + poll) % 5) }));
      return r.fulfill({ contentType: "application/json", body: JSON.stringify({ ...graph, edges }) });
    });
    await page.route("**/api/v1/namespaces**", (r) =>
      r.fulfill({ contentType: "application/json", body: JSON.stringify({ namespaces, window: graph.window }) }),
    );
    await page.route("**/api/v1/nodes/**", (r) => {
      const id = decodeURIComponent(new URL(r.request().url()).pathname.split("/").pop()!);
      const node = graph.nodes.find((n) => n.id === id) ?? graph.nodes[0];
      const dep = (e: (typeof graph.edges)[number], other: string) => ({
        ...e,
        node_id: other,
        label: graph.nodes.find((n) => n.id === other)?.label ?? other,
      });
      r.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          node,
          incoming: graph.edges.filter((e) => e.target_id === id).map((e) => dep(e, e.source_id)),
          outgoing: graph.edges.filter((e) => e.source_id === id).map((e) => dep(e, e.target_id)),
          window: graph.window,
        }),
      });
    });
    await page.route("**/config.json", (r) => r.fulfill({ status: 404, body: "" }));

    const row: Record<string, unknown> = { nodes: graph.nodes.length, edges: graph.edges.length };
    const t0 = Date.now();
    await page.goto("/");
    try {
      await page.locator(".react-flow__node").first().waitFor({ timeout: HANG_MS });
      row.firstPaintMs = Date.now() - t0;
    } catch {
      row.firstPaintMs = "hung";
      results.push(row);
      return;
    }

    // Past the cap the canvas opens grouped by namespace; the per-workload view is the one that
    // has to draw every edge.
    const workloads = page.getByRole("button", { name: "Workloads", exact: true });
    if ((await workloads.getAttribute("aria-pressed")) !== "true") {
      const s = await settle(
        page,
        `[...document.querySelectorAll(".topology-toolbar button")].find(b => b.textContent === "Workloads").click()`,
      );
      row.workloadsView = s ?? "hung";
      if (!s) {
        results.push(row);
        return;
      }
    }
    row.domEdges = await page.locator(".react-flow__edge").count();
    row.domLabels = await page.locator(".react-flow__edge-text").count();
    row.frameMs = await frameResponse(page);

    // A node near the middle of the pane, so it is one a person could actually click.
    const select = await settle(
      page,
      `const pane = document.querySelector(".react-flow").getBoundingClientRect();
       const cx = pane.left + pane.width / 2, cy = pane.top + pane.height / 2;
       const nodes = [...document.querySelectorAll(".react-flow__node")];
       nodes.sort((a, b) => {
         const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect();
         return Math.hypot(ra.x - cx, ra.y - cy) - Math.hypot(rb.x - cx, rb.y - cy);
       });
       nodes[0].click();`,
    );
    row.select = select ?? "hung";
    row.deselect =
      (await settle(page, `document.querySelector(".react-flow__pane").click()`)) ?? "hung";

    const box = (await page.locator(".react-flow").boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    const zoom = idle(page, 1_500);
    for (let i = 0; i < 5; i++) {
      await page.mouse.wheel(0, -120);
      await page.waitForTimeout(100);
    }
    row.zoom = await zoom;
    row.idlePolls = await idle(page, 11_000);

    results.push(row);
  });
}

test.afterAll(() => {
  // eslint-disable-next-line no-console
  console.log(JSON.stringify(results, null, 2));
  const dir = new URL("./results/", import.meta.url);
  mkdirSync(dir, { recursive: true });
  writeFileSync(new URL(`${LABEL}.json`, dir), JSON.stringify(results, null, 2) + "\n");
});
