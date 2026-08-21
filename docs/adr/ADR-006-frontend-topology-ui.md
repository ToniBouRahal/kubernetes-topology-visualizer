# ADR-006: Frontend — Interactive Topology UI

- **Status:** Accepted for implementation
- **Date:** 2026-08-12
- **Parent:** ADR-001 §5.6 · Source of truth §13, §14
- **Component path:** `frontend/`
- **Owning phases:** Phase 2 (live graph), Phase 3 (history + compare), Phase 4 (completeness + a11y)
- **Stack:** React, TypeScript, Vite, React Flow, Dagre

## 1. Scope

The browser application: typed API client, graph rendering, layout, filters, details, comparison
mode, and all loading/empty/error/truncated states. Response shapes belong to ADR-003; the nginx
image and Service belong to ADR-007.

## 2. What the prototype proved, and what must change

`src/App.jsx` is 262 lines of JavaScript in a single component. It proves React Flow + Dagre renders
this graph shape acceptably. Its structure and several of its specific choices must not carry over.

### Reuse deliberately

| Asset | Location | Why |
|---|---|---|
| Dagre `rankdir: 'LR'`, `ranksep: 120`, `nodesep: 70` | `App.jsx:23` | Tuned for this graph's shape; a sensible starting point. |
| The centring correction `x - W/2, y - H/2` | `App.jsx:30` | Dagre returns centres, React Flow wants top-left. Easy to get wrong. |
| Five-second polling | `App.jsx:123` | Matches ADR-001 §5.6. |
| Empty state with a "generate traffic" hint | `App.jsx:174-184` | The right instinct — an empty graph is the normal first state, not an error. |

### Must change

| # | Prototype behaviour | Location | Required behaviour |
|---|---|---|---|
| F1 | JavaScript, no types | whole file | TypeScript with types generated from OpenAPI (ADR-003 D-3.1) |
| F2 | Parses IDs to derive namespace | `App.jsx:48` | IDs are opaque; use returned `namespace`/`label` fields |
| F3 | Colour is the only encoding of kind | `App.jsx:36-64` | Shape and icon carry kind; colour reinforces only |
| F4 | Namespace palette hardcoded to `demo`/`topology`/`kube` | `App.jsx:49-53` | Namespaces are discovered at runtime; assignment must be stable and deterministic |
| F5 | Single 262-line component, inline styles | whole file | Feature folders per ADR-001 §5.8 |
| F6 | Layout recomputed from scratch every poll | `App.jsx:109` | Stable layout: unchanged nodes keep positions (ADR-001 §5.6) |
| F7 | On fetch error the graph is not preserved; `error` shown but nodes keep last state only incidentally | `App.jsx:114-116` | Explicitly retain the last successful graph on transient failure |
| F8 | No request cancellation — overlapping polls can apply out of order | `App.jsx:79-119` | `AbortController`; cancel stale requests |
| F9 | `animated: true` on every edge | `App.jsx:100` | Continuous animation on hundreds of edges burns frame budget; the 100 ms target forbids it |
| F10 | No filters, search, details panel, history, or compare | — | Required by ADR-001 §5.6 |
| F11 | Fixed `lastSeconds=300` | `App.jsx:83` | Presets + custom range + compare periods |
| F12 | No keyboard access, no focus styling, no contrast guarantee | — | WCAG AA (ADR-001 §5.6) |

## 3. Decisions

### D-6.1 — Types are generated, never hand-written

`frontend/src/api/generated/` is produced from `contracts/openapi.json` and is never edited. A
hand-written wrapper in `src/api/` adds query construction, `AbortController` wiring, and error
normalisation on top. ADR-001 §5.6 forbids duplicating unvalidated payload types by hand.

### D-6.2 — Layout stability

Stable layout is a named requirement and the least obvious thing in this component. Dagre is
deterministic for identical input, but node **ordering** changes between polls will move everything.

1. Sort nodes and edges by the deterministic key from ADR-003 D-3.6 before handing them to Dagre.
2. Cache positions keyed by node ID. On each poll, re-run layout only if the set of node IDs or edge
   keys changed; otherwise reuse cached positions and update only labels and styling.
3. When the set does change, keep surviving nodes at their cached positions and lay out only the
   new ones where possible, so one new edge does not rearrange the whole graph mid-demo.

This is the difference between a graph a viewer can follow and one that reshuffles every five
seconds.

### D-6.3 — Visual encoding without relying on colour

| Concept | Primary cue | Secondary cue |
|---|---|---|
| Node kind | shape + icon | colour |
| Namespace | grouping/border treatment | colour |
| `EXTERNAL` | distinct shape + explicit label | colour |
| Unresolved | distinct shape + label | colour |
| Diff `NEW` | solid stroke + `NEW` text badge | green |
| Diff `REMOVED` | dashed stroke + `REMOVED` text badge | red |
| Diff `CHANGED` | thick stroke + `CHANGED ±N%` text badge | amber |
| Direction | arrowhead | — |
| Traffic intensity | edge width (capped log scale) | — |

Every state readable without colour. Namespaces must not become deeply nested subgraphs — ADR-001
§5.6 warns against unreadable nesting; use border/grouping treatment instead.

### D-6.4 — Traffic intensity

Edge width is a **capped logarithmic** function of the metric: `width = clamp(minW, minW + k *
log1p(value), maxW)`. Byte volume when available, connection count otherwise — and the UI must name
which metric is in use, in the legend and in the edge detail. Silently switching metrics between
edges is misleading; ADR-001 §7 Phase 4 requires the metric to be named explicitly.

Edge labels show `TCP:<port>`, connection count, and byte volume when available and zoom permits.

### D-6.5 — Modes

Three explicit modes in the header: **live**, **history**, **compare**.

- Live: polls every 5 s, pause/resume, last-update timestamp, connection status.
- History: preset (`1m`, `5m`, `15m`, `1h`, `6h`, `24h`) or custom `from`/`to`; polling stops.
- Compare: baseline and current period pickers; renders diff classifications; `include_unchanged`
  toggle.

Mode is visible at a glance — a paused historical view that looks live is a demo hazard.

### D-6.6 — Layout and states

Header (status, mode, range, refresh/pause, last update) · left filter panel (namespace, kind,
external visibility, search) · centre canvas · right details panel (opens on node/edge selection) ·
compact legend with counts.

Four states, each actionable rather than decorative:

| State | Requirement |
|---|---|
| Loading (first) | skeleton; never a blank canvas |
| Empty | "no communication observed in this window" + how to generate traffic + a link to widen the window |
| Truncated | banner naming the cap, the counts, and how to narrow filters |
| Error | keep the last successful graph visible, show a non-blocking banner, keep retrying |

### D-6.7 — Performance and accessibility

Target: no UI freeze over 100 ms during a polling update at 500 nodes / 2,000 edges, usable at
1280×720.

- No per-edge continuous animation (F9).
- Memoise node/edge transforms; avoid rebuilding the whole React Flow array on unrelated state
  changes.
- Filtering happens server-side via query parameters; the client does not fetch everything and
  filter locally.
- Keyboard: every control reachable and operable; visible focus rings; the graph offers a
  keyboard-navigable node list as the accessible equivalent of clicking a node.
- Contrast meets WCAG AA in the chosen theme.

## 4. Implementation guide

```text
frontend/src/
  api/
    generated/        from openapi.json — never hand-edited
    client.ts         query building, AbortController, error normalisation
  features/
    graph/            React Flow canvas, node/edge components, layout cache (D-6.2)
    filters/          namespace, kind, search, external/unresolved toggles
    details/          node and edge detail panels
    timerange/        presets, custom range, compare pickers
  components/         header, legend, banners, empty/error/truncated states
  styles/
```

Phase 2: typed client → canvas with live polling → EXTERNAL node → namespace filter + search →
detail panel → states. Phase 3: history picker, compare mode, diff styling. Phase 4: stable layout
cache, accessibility pass, legend, truncation, intensity styling, component tests in CI.

## 5. Skills and plugins for this component

### Required

| Skill / plugin | When | Why |
|---|---|---|
| **`frontend-design`** | Phase 2 first render, and the whole of Phase 4 | This is the component-specific design skill. It produces polished, non-generic interfaces — directly relevant because ADR-001 §5.6 specifies a real product layout (header/filter/canvas/details/legend) rather than a demo page. `/plugin install frontend-design@claude-plugins-official` |
| **`typescript-lsp`** | All frontend work | Generated API types are large and structural; without a language server, type errors against the generated client surface only at build time. `/plugin install typescript-lsp@claude-plugins-official` |
| **`dataviz`** — **scoped** | Choosing the palette, the legend, the traffic-intensity scale, and verifying colour-blind and dark/light legibility | Load it for **encoding and colour** decisions: categorical palettes, legend design, and the accessible-contrast validator. **Do not** use it for node-link layout — that is Dagre's job (D-6.2) and dataviz's chart-form heuristics do not apply to a topology graph. |
| **`playwright`** | Phase 2 onward | Phase 2's acceptance criterion ("traffic appears in the browser within 20 seconds", "a smoke test checks the named expected demo edges") is a browser assertion, not an API assertion. `/plugin install playwright@claude-plugins-official` |
| **`context7`** | Before writing React Flow, Dagre, or Vite configuration | React Flow's package name and API changed across major versions — the prototype imports `reactflow`, which is the v11 package (`App.jsx:2`, `App.jsx:9`); v12 publishes as `@xyflow/react` with different hook and style imports. Verify the current package and API before writing components rather than copying the prototype's imports. |
| **`topology-contract`** (ADR-003) | Any change in `src/api/` | Enforces the opacity rule that F2 violates. |

### Situational

- **`/code-review`** on the Phase 4 accessibility and layout-cache work.
- **`adr-guard`** — the UI is where scope creep is most tempting (live streaming, per-pod drilldown,
  Prometheus panels). All three are deferred in ADR-001 §13.
- **`/run`** — to launch the dev server and confirm a change renders.

### Do not use

- **`artifact-design`, `artifact-capabilities`, `artifact-diagramming`** — these govern
  Claude-published artifact pages. This is a Vite-built React application deployed as a container.
  Applying artifact guidance here produces a single-file HTML page, which is the wrong architecture.
  (`artifact-diagramming` is permitted only for `docs/architecture.md` diagrams — see ADR-008.)
- **`dataviz` for layout or chart selection** — see the scoping note above.
- **`claude-api`** — no LLM in this product.

## 6. Test requirements (ADR-001 §8 row 6)

| ID | Test | Kind |
|---|---|---|
| T-6.1 | API mapping: `graph.response.json` fixture → expected nodes/edges | unit |
| T-6.2 | Layout stability: identical response twice → identical positions; one added node leaves existing positions unchanged | unit |
| T-6.3 | Filters and search produce deterministic visible results | component |
| T-6.4 | Mode switching live → history → compare issues the right queries and stops polling in history | component |
| T-6.5 | Diff styling: each class distinguishable with colour removed (assert the text badge and stroke style, not the colour) | component |
| T-6.6 | Transient API failure retains the last successful graph and shows a non-blocking banner | component |
| T-6.7 | Empty, loading, truncated states render with their actionable content | component |
| T-6.8 | Selection opens the details panel with namespace, dependencies, ports, counts, first/last seen | component |
| T-6.9 | Every interactive control is keyboard reachable with a visible focus indicator | component |
| T-6.10 | Stale poll responses arriving out of order do not overwrite newer data | unit |
| T-6.11 | Expected demo edges appear in the browser within 20 s of traffic generation | E2E (Playwright) |
| T-6.12 | A controlled topology change appears in compare mode as the expected `NEW`/`REMOVED` edge | E2E (Playwright) |

## 7. Acceptance criteria

Phase 2: traffic visible within 20 s; direction, port, connection count, first/last seen shown;
deterministic filters and search; transient failure leaves the last graph visible; automated smoke
test passes.

Phase 4: usable at 1280×720; all controls keyboard reachable; comparison understandable without
colour; edge thickness uses the named metric; details show the full field set; component tests pass
in CI.

## 8. Consequences

- **Polling has a visible latency floor.** Up to 10 s agent aggregation + up to 5 s poll interval
  means "within 20 seconds" is the honest promise. Do not present the graph as real-time; SSE is
  deferred (ADR-001 §13).
- **The layout cache adds state.** More complexity than re-layout-every-poll, bought deliberately:
  ADR-001 names layout stability as a requirement, and an unstable graph is unusable in a live
  demonstration.
- **Server-side filtering means a round trip per filter change.** Accepted — it keeps the client
  honest about the truncation caps and avoids shipping a 2,000-edge payload to filter down to ten.
- **Non-colour encoding constrains the visual design.** Shape and badge vocabulary must be defined
  once in the legend and reused; this is a real design constraint, not a decoration, and is why
  `frontend-design` and the scoped `dataviz` guidance are both loaded for this component.

## 9. Implementation tracker

Mirrors [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md). `[ ]` open · `[~]` in progress · `[x]` done ·
`[!]` blocked · `[-]` dropped with reason.

### Phase 2 — live graph

- [x] **P2-F1** Vite + TypeScript scaffold; generated client from `openapi.json` — D-6.1 (fixes F1)
- [x] **P2-F0** Confirm the React Flow package and major version before writing components — §5 (`reactflow` v11 vs `@xyflow/react` v12)
- [x] **P2-F2** React Flow canvas + Dagre layout, centring correction preserved — D-6.2
- [x] **P2-F3** Polling with `AbortController`; retain last good graph on error — D-6.6 (fixes F7, F8) · tests T-6.6, T-6.10
- [x] **P2-F4** Time presets, namespace filter, search — D-6.5 (fixes F11) · test T-6.3
- [x] **P2-F5** EXTERNAL node with shape + label, not colour alone — D-6.3 (fixes F3)
- [x] **P2-F6** Node and edge detail panel — D-6.6 · test T-6.8
- [x] **P2-F7** Loading, empty, error states with actionable content — D-6.6 · test T-6.7
- [x] **P2-F8** Remove per-edge continuous animation — D-6.7 (fixes F9)
- [x] **P2-T4** Frontend unit/component tests — §6
- [ ] **P2-T5** Playwright E2E: expected demo edges within 20 s — tests T-6.11, T-8.2 · **→ chrome** for first bring-up

**Phase 2 gate** (ADR-001 §7): traffic visible within 20 s · direction, port, count, first/last seen
shown · deterministic filters and search · transient failure leaves the last graph visible · smoke
test passes.

### Phase 3 — history and comparison

- [ ] **P3-F9** History picker: presets + custom range; polling stops in history mode — D-6.5 · test T-6.4
- [ ] **P3-F10** Compare mode: baseline/current pickers, `include_unchanged` toggle — D-6.5
- [ ] **P3-F11** Diff styling: stroke pattern + text badge per class, readable without colour — D-6.3 · test T-6.5

### Phase 4 — completeness and accessibility

- [ ] **P4-F12** Detail panels: incoming/outgoing dependencies, ports, counts, timestamps — D-6.6 · test T-6.8
- [ ] **P4-F13** Layout position cache; unchanged nodes keep positions across polls — D-6.2 · test T-6.2
- [ ] **P4-F14** Keyboard reachability, visible focus, WCAG AA contrast — D-6.7 · test T-6.9 · **→ chrome**
- [ ] **P4-F15** Legend + truncation banner + capped-log intensity with the metric named — D-6.4
- [ ] **P4-F16** Verify usability at 1280×720 — D-6.7 · **→ chrome**
- [ ] **P4-F17** Byte-based intensity **only if** P4-X1 passes; otherwise connection count, named explicitly
- [ ] **P4-T10** Component tests running in CI — ADR-008 D-8.3

**Phase 4 gate** (ADR-001 §7): usable at 1280×720 · all controls keyboard reachable · comparison
understandable without colour · thickness uses a named metric · details complete · tests in CI.

### Standing invariants — re-verify at every phase gate

- [ ] No component parses a node ID to derive namespace or label (F2)
- [ ] Nothing in `src/api/generated/` is hand-edited
- [ ] Every state distinguishable with colour removed — test T-6.5
- [ ] No polling update freezes the UI beyond 100 ms at 500 nodes / 2,000 edges
