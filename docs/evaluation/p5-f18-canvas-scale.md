# P5-F18: the canvas at its stated scale ceiling

**Status: largely closed, one residual stated below.** The per-workload view now draws 500 nodes /
2,000 edges. First paint is about 1 s, and clicks, zooms and polls stay under ADR-006's 100 ms. The
400-edge cap is raised to 2,000. The remaining gap: a topology change at that size still re-runs
layout on the main thread (about 0.75 s).

## The Phase 5 diagnosis was wrong

Phase 5 recorded that 1,000 edges never painted and blamed React Flow's DOM. It called Dagre "not
the bottleneck", based on `layout.test.ts` timing a random graph at 500 / 2,000 in about 250 ms.

Timing `layoutGraph` alone on the shape `scripts/seed-scale.py` sends through the real ingest
path, which is the same shape the browser measurement used:

| layout (Node, no browser) | 300 edges | 600 | 800 | 1,000 | 2,000 |
|---|---:|---:|---:|---:|---:|
| Dagre defaults: network-simplex ranking, crossing minimisation | 185 ms | 923 ms | **24 s** | **> 150 s** | **> 150 s** |
| tight-tree ranking, crossing minimisation on | | | 903 ms | | 8.0 s |
| tight-tree ranking, crossing minimisation off | 70 ms | | 202 ms | 253 ms | 811 ms |

The browser was never rendering slowly. It was stuck inside one synchronous `dagre.layout` call,
which is why a CPU profile over CDP could not even be stopped. A profile in Node puts 97% of the time
inside `dagre.layout`. The table above shows which of its options accounts for it.

Graph **shape** decides Dagre's cost. The random wiring in `layout.test.ts` is spread across many
ranks, while a dense set of workloads calling Services is two ranks with heavy overlap. That is
why the unit test passed while the browser hung.

## What changed

| change | where | effect |
|---|---|---|
| Past 300 edges, Dagre uses `tight-tree` ranking and skips the crossing-minimisation sweeps | `layout.ts` `FAST_LAYOUT_EDGES` | removes the hang; below 300 edges the layout is unchanged |
| Node and edge objects are reused when what they draw is unchanged | `TopologyCanvas.tsx` `reuse()` | a poll or a click no longer re-renders every element |
| Dimming is a class on the canvas plus a mark on the neighbourhood | `TopologyCanvas.tsx`, `app.css` | a click restyles the neighbourhood, not the whole graph |
| Past 100 drawn edges, labels appear only for the selection's neighbourhood and the hovered edge | `TopologyCanvas.tsx` `LABEL_ALL_EDGES` | two fewer DOM elements per edge; the demo graph is still fully labelled |
| `onlyRenderVisibleElements` | `TopologyCanvas.tsx` | off-screen elements are not mounted (1,010 of 2,000 edges at the default view) |
| Cap raised from 400 to 2,000, matching the backend's default `GRAPH_MAX_EDGES` | `renderBudget.ts` | the cap still guards an operator who raises the backend limit |
| Grouping by namespace by default past 400 edges now has its own constant | `TopologyCanvas.tsx` `GROUP_BY_DEFAULT_EDGES` | same behaviour as before, now a readability choice rather than a performance one |

## Method

`frontend/bench/` ([README](../../frontend/bench/README.md)). It uses a production build, headless
Chromium (Playwright 1.62.1), 1280x720, and React Flow 12.11.3, on an AMD Ryzen 7 5800H with 16
threads and 19 GiB of RAM. The API is mocked with a seeded synthetic graph shaped like
`seed-scale.py`, so these numbers cover the browser only.

Long tasks are main-thread tasks over 50 ms, so **0** means no task crossed 50 ms. Raw results are in
`frontend/bench/results/`.

## Results

**Before**: `HEAD`, with its 400-edge cap (`baseline-cap400.json`):

| graph | first paint (grouped past 400 edges) | Workloads view (400 drawn) | select, worst task | each idle poll, worst task |
|---|---:|---:|---:|---:|
| 100 / 300 | 556 ms | (opens there) | 55 ms | 72 ms |
| 170 / 1,000 | 809 ms | 653 ms, worst task 545 ms | 65 ms | 118 ms |
| 500 / 2,000 | 778 ms | 1,117 ms, worst task 971 ms | 107 ms | 115 ms |

With the cap lifted (`baseline-uncapped.json`), 1,000 edges did not paint within 60 s. This matches
Phase 5.

**After**: shipped configuration, every edge drawn (`stage1.json`):

| graph | first paint (grouped past 400 edges) | Workloads view | select, worst task | deselect, worst task | zoom | idle polls |
|---|---:|---:|---:|---:|---:|---:|
| 100 / 300 | 448 ms | (opens there) | 0 | 0 | 0 | 0 |
| 170 / 1,000 | 646 ms | 281 ms, worst task 237 ms | 0 | 61 ms | 0 | 0 |
| 500 / 2,000 | 654 ms | 817 ms, worst task 729 ms | 82 ms | 68 ms | 0 | 0 |

Opening straight into the per-workload view with every edge drawn (cap lifted, `stage1-uncapped.json`):
first paint is 493 ms at 1,000 edges and 1,065 ms at 2,000.

**After, with live counts**: every poll changes every edge's count (`stage1-live.json`), at 500 /
2,000: select 98 ms, deselect 79 ms, idle polls 0. With the cap lifted and the view opening ungrouped
(`stage1-uncapped-live.json`), the worst idle-poll task was 60 ms.

## What remains

- **Layout still runs on the main thread.** Switching to the per-workload view at 2,000 edges
  blocks for about 0.73 s once. A poll that adds or removes a workload re-runs layout at the same
  cost. Polls that only change counts skip layout entirely, as before. The fix is to move layout
  into a Web Worker.
- **Large graphs have more crossings.** The fast layout skips crossing minimisation. Below 300
  edges nothing changed, so the demo graph looks exactly as it did.
- **Labels past 100 edges appear on request.** Select a component or hover an edge to see them.
- **Responsive is not the same as readable.** 2,000 edges on one canvas is still a dense picture,
  which is why graphs over 400 edges still open grouped by namespace.
- **Synthetic data, mocked API, one machine.** Phase 5 measured real ingested data. The graph here
  has the same shape and size but is served by a mock, so the backend's share is measured
  separately (query p95 62 ms).
