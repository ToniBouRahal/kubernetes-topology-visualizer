# Canvas scale benchmark

Measures the topology canvas at the sizes ADR-001 §6 names, in a real browser, against a
production build. It was written to settle P5-F18; the method and results are in
[`docs/evaluation/p5-f18-canvas-scale.md`](../../docs/evaluation/p5-f18-canvas-scale.md).

It is not part of `npm test` or the cluster E2E suite. It needs no cluster: the API is mocked with
a seeded synthetic graph shaped like `scripts/seed-scale.py` produces, so what it measures is the
browser and nothing else. The API side is measured by `scripts/experiments.sh latency`.

```bash
cd frontend
npx playwright test -c bench/playwright.config.ts
```

| variable | default | meaning |
|---|---|---|
| `BENCH_SIZES` | `100x300,170x1000,500x2000` | `nodes x edges`, comma-separated |
| `BENCH_LABEL` | `run` | results land in `bench/results/<label>.json` |
| `BENCH_LIVE` | unset | `1`: every poll carries new connection counts, as a live cluster's does |
| `BENCH_HANG_MS` | `60000` | past this an action is recorded as `"hung"` rather than waited on |
| `VITE_MAX_RENDERED_EDGES` | unset | build-time override of the canvas edge cap, to measure past it |

Per size it records:

- `firstPaintMs`: navigation to the first node on the canvas
- `workloadsView`: switching from the namespace-grouped view (the default past 400 edges) to the
  per-workload view that draws every edge
- `select` / `deselect`: clicking a node near the middle of the pane, then the empty pane
- `zoom`: five wheel steps
- `idlePolls`: 11 s of doing nothing, which covers two 5 s polls

Each action reports `settledMs` (the action to the last DOM change in the canvas, once it has been
quiet for 1 s), and `longTaskMs` / `worstTaskMs` (main-thread tasks over 50 ms). **`worstTaskMs` is
the number to compare with ADR-006's 100 ms budget**: `settledMs` also counts the next poll if one
lands inside the quiet window.
