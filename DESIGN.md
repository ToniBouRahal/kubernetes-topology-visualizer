---
name: Runtime Topology
description: A passive-listening console for observed Kubernetes traffic — near-black instrument, one phosphor accent per temporal mode.
colors:
  ink: "#070b0a"
  panel: "#0d1412"
  panel-high: "#131c19"
  line: "#1d2925"
  line-strong: "#33473c"
  text: "#dfeee7"
  text-dim: "#8fae9f"
  text-faint: "#71937f"
  signal-live: "#39e3a3"
  signal-history: "#e0a94d"
  ns-1-teal-cyan: "#3fb8a8"
  ns-2-amber: "#d9a441"
  ns-3-violet: "#a08de0"
  ns-4-rose: "#e07a92"
  ns-5-sky-blue: "#6fabe0"
  ns-6-lime: "#8bcf5e"
  external: "#b3a08a"
  edge: "#34493e"
  edge-strong: "#5c8a75"
  ok: "#39e3a3"
  warn: "#e0a94d"
  danger: "#e07a92"
typography:
  title:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: 1.45
    letterSpacing: "-0.01em"
  body:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.45
    letterSpacing: "normal"
  label:
    fontFamily: "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "0.75rem"
    fontWeight: 600
    lineHeight: 1.45
    letterSpacing: "0.1em"
  measured:
    fontFamily: "ui-monospace, 'SF Mono', 'Cascadia Mono', 'JetBrains Mono', 'IBM Plex Mono', Menlo, monospace"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.45
    letterSpacing: "normal"
rounded:
  sm: "3px"
  md: "5px"
spacing:
  1: "4px"
  2: "8px"
  3: "12px"
  4: "16px"
  5: "24px"
  6: "32px"
components:
  button-primary:
    backgroundColor: "{colors.panel-high}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    padding: "4px 12px"
  button-primary-hover:
    backgroundColor: "#182420"
  segmented-control-on:
    backgroundColor: "{colors.panel-high}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
  contact-dot:
    backgroundColor: "{colors.signal-live}"
    rounded: "50%"
    width: "8px"
    height: "8px"
  node-card:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
---

# Design System: Runtime Topology

## Overview

**Creative North Star: "The Passive Instrument"**

Runtime Topology reads as a listening console, not a business dashboard — the direct visual consequence of the product's own thesis (the eBPF agent never probes, injects, or asks anything; it only observes what already happened on the wire). The ground is true near-black, not blue-slate: a screen, not a pane of glass. One phosphor colour lives on it at a time, cold cyan-green while the reading is live, warm amber once it is historical or comparative — two states of one instrument rather than a brand palette. Namespace identity keeps its own, separate colour channel, carried forward unchanged in spirit from before this redesign (a tested product constraint, not a new invention): kind is always written in words on every node, never implied by shape or colour, so colour stays free to mean namespace alone.

The system rejects the devices a dashboard would reach for by default: no drop shadows as decoration, no card-stack elevation, no downloaded display face, no glyph iconography. What replaces them is native to the instrument metaphor — a faint concentric range-ring texture on the canvas, a graduated tick-rule for the observed time span, tabular monospace for every measured value, tracked-out uppercase labels for bezel lettering. Motion is rationed to exactly two elements, both O(1) regardless of graph size, both gated on "is this reading genuinely live and unpaused right now."

**Key Characteristics:**
- True near-black ground (`--ink #070b0a`), not a dark-blue "premium SaaS" slate.
- Exactly one accent hue live at a time, mode-scoped via a single CSS override, never per-component logic.
- Namespace colour and temporal-mode colour are two independent channels that never blend.
- No web fonts anywhere — system sans and system mono stacks only.
- Measured values are always tabular monospace; everything else is sans.
- Two animated elements total in the entire app, both gated, both bounded.

## Colors

Two independent colour systems share the ground: a two-state instrument accent keyed to temporal mode, and a six-hue namespace-identity set keyed to cluster namespace. Every text and non-text-focus colour pairing is enforced by a build-time test (`frontend/tests/contrast.test.ts`) that reads `tokens.css` directly and fails the suite below 4.5:1 (text) or 3:1 (the focus ring), checked against all three surfaces a token can land on — `--ink`, `--panel`, and `--panel-high` (the lightest, and therefore binding, surface).

### Primary
- **Live Signal — cold cyan-green** (`#39e3a3`): the instrument's accent while a reading is genuinely live. Drives the header mark's ring, the focus ring, text selection, and the canvas's range-ring texture and sweep overlay whenever `.app` is not in `data-mode="history"` or `"compare"`.
- **History Signal — warm amber** (`#e0a94d`): the same accent, swapped in for History and Compare. Applied by exactly one CSS scope, `.app[data-mode="history"], .app[data-mode="compare"]`, which overrides `--signal`/`--signal-rgb` — never per-component conditionals. This is "the same instrument at night," not a second brand colour.

### Neutral
- **Ink** (`#070b0a`): the canvas — the instrument's own screen.
- **Panel** (`#0d1412`): raised surfaces — header, side panels, cards — the console's bezel.
- **Panel High** (`#131c19`): controls and hover states, one step brighter than Panel.
- **Line** (`#1d2925`) / **Line Strong** (`#33473c`): hairline structure and emphasized structure (active borders, tick marks).
- **Text** (`#dfeee7`), **Text Dim** (`#8fae9f`), **Text Faint** (`#71937f`): the three-step text ramp, each independently AA-verified against all three surfaces above.

### Namespace identity (ns-1..6)
- **ns-1 teal-cyan** (`#3fb8a8`), **ns-2 amber** (`#d9a441`), **ns-3 violet** (`#a08de0`), **ns-4 rose** (`#e07a92`), **ns-5 sky-blue** (`#6fabe0`), **ns-6 lime** (`#8bcf5e`): assigned deterministically from namespace name so a namespace keeps its hue across reloads. This is a pre-existing, tested product constraint (kind is written, never colour- or shape-coded) carried through this redesign, not introduced by it — only the hues themselves were re-tuned and re-measured against the new near-black ground.

### Semantic
- **External** (`#b3a08a`): traffic leaving the cluster boundary — warm and desaturated, deliberately not alarming.
- **Ok** (`#39e3a3`): backend-reachability status ("Connected"/"No data"). Same hex as Live Signal today, but a distinct token — see the Signal/Status Rule below.
- **Warn** (`#e0a94d`) / **Danger** (`#e07a92`): failed/aborted-connection cues and error banners.

### Named Rules
**The Signal/Status Rule.** `--ok` is never aliased to `--signal`. Whether the backend is reachable is a fact independent of which temporal mode is being viewed, so "Connected" reads the same cyan-green in History or Compare as it does in Live — only the mode-chrome accent (header mark, focus ring, selection, canvas texture) swaps to amber. Merging the two tokens would make amber ambiguous between "this reading is historical" and "the backend is unreachable." A future edit must keep two tokens even though they currently share a hex value.

**The Namespace-Owns-Colour Rule.** Colour encodes namespace only. Kind (Deployment, Service, StatefulSet, DaemonSet, External) is always carried by a written label on the node, never by colour or shape alone — this predates the redesign and is not renegotiable by a future surface.

## Typography

**Body/Label Font:** `ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", sans-serif`
**Measured Font:** `ui-monospace, "SF Mono", "Cascadia Mono", "JetBrains Mono", "IBM Plex Mono", Menlo, monospace`

**Character:** No downloaded face, anywhere — a hard functional constraint (the offline `kind` demo must not reflow if a web font fails to load), not a style preference. Console "bezel lettering" character comes entirely from letter-spacing, weight, and case, layered onto the system sans stack. Labels are tracked-out uppercase, not a condensed face — there is no `font-stretch: condensed` claim anywhere, since the no-web-fonts constraint can't reliably render one.

### Hierarchy
- **Title** (600, `1.125rem`/step-2, 1.45, `-0.01em`): the header wordmark, "Runtime Topology" — the only place letter-spacing tightens instead of tracks out.
- **Body** (400, `0.8125rem`/step-0, 1.45): default UI text, node names, dependency rows.
- **Label** (600, `0.75rem`/step-−1, 1.45, `0.1em` tracking, uppercase): section headers, kind tags, tab segments, button text — the tracked-out uppercase device that stands in for a condensed display face.
- **Measured** (mono, tabular-nums, `0.8125rem`): every quantity that is a reading rather than a label — connection counts, timestamps, latency, edge counts. The mono/sans split is itself the signal: "this is a measurement."

### Named Rules
**The Mono-Means-Measured Rule.** Monospace is reserved for values read off the instrument (counts, timestamps, durations). Anything else — labels, names, copy — stays in the sans stack. Do not use mono for decorative effect.

**The No-Web-Fonts Rule.** System sans and system mono stacks only, everywhere, permanently. This is a demo-reliability constraint (an offline `kind` cluster must render identically), not an aesthetic choice, and it binds future work exactly as it bound this one.

## Layout

Fixed three-column body at the 1280×720 demo baseline: a 232px left filter panel, a flexible canvas, and a 320px right details panel (`--panel-left`, `--panel-right`), under a 48px header and a 34px observation strip. Below 1100px width the right panel detaches into an absolutely-positioned overlay rather than squeezing the canvas further. Spacing runs an 8-step scale from 4px to 32px (`--s-1`…`--s-6`); panel sections use 16px internal padding as the default rhythm, with 8px between related inline controls.

## Elevation & Depth

Flat by default — no ambient drop shadows on cards, panels, or nodes. Depth is conveyed by tonal layering across three ground steps (`--ink` → `--panel` → `--panel-high`) plus 1px hairline borders (`--line` / `--line-strong`), not by shadow. The one place a shadow is load-bearing rather than decorative is the header mark's `drop-shadow` phosphor bloom, which exists to make the mark read as lit rather than printed. Floating overlays that must visually detach from the surface below (banners, the collapsed right panel on narrow viewports) use a soft directional shadow for that specific purpose only.

### Shadow Vocabulary
- **Phosphor bloom** (`filter: drop-shadow(0 0 3px rgba(var(--signal-rgb), 0.7))`): the header mark only, always tied to the live accent colour.
- **Banner lift** (`box-shadow: 0 6px 20px rgb(0 0 0 / 0.45)`): stacked status banners over the canvas, so they read as detached from the graph beneath.
- **Panel overlay lift** (`box-shadow: -8px 0 24px rgb(0 0 0 / 0.5)`): the right details panel when it detaches into an overlay under 1100px.

### Named Rules
**The Flat-By-Default Rule.** Surfaces are flat at rest. A shadow appears only when an element must read as detached from — not merely stacked on — the surface beneath it (an overlay panel, a banner, the one instrument-bloom exception). Never add an ambient card shadow for polish.

## Shapes

Rectangular, hairline-bordered cards throughout (`--radius: 5px` default, `--radius-sm: 3px` for controls and inputs) — no pill shapes, no large radii. The one deliberate departure from rectangles: every dot that stands for a discrete reading — namespace chips, status dots, the header mark, the dependency-failure marker — is circular ("a contact reads as a station on a scope, not a swatch"). Nodes on the canvas keep a plain outlined-rectangle silhouette with no shape vocabulary by kind; this is a re-affirmed constraint (a shape vocabulary was tried and deliberately removed from this product before this redesign), not an oversight.

### Named Rules
**The Contact-Dot Rule.** Every discrete-state indicator (namespace identity, connection status, failed/aborted markers) is drawn as a small circular dot with a soft currentColor glow, never a square swatch or a bar. This is the one consistent departure from the system's otherwise rectangular geometry.

## Components

### Buttons
- **Shape:** 3px radius, 1px `--line` border.
- **Default:** `--panel-high` background, inherited text colour, `4px 12px` padding.
- **Hover:** border brightens to `--line-strong`, background nudges to `#182420`.
- **Disabled:** 0.5 opacity, default cursor.
- **Segmented control** (Live / History / Compare): borderless siblings joined into one pill-free strip, `--ink` background at rest, `--panel-high` + `--line-strong` border when active (`.seg--on`); rounded only at the strip's own outer corners.

### Chips / Dots (signature component)
Circular "contact dots," not rounded-square swatches — see the Contact-Dot Rule. A namespace dot is `8px`, currentColor, with a soft `color-mix` halo; a status dot is `7px` with a `box-shadow: 0 0 4px currentColor` glow. Selection state on list rows is communicated by a drawn two-stroke checkmark (`border-left`/`border-bottom`, rotated 45deg) appended after the row label — never a Unicode glyph, never colour alone.

### Cards / Containers (node cards, panels)
- **Corner Style:** 3–5px radius.
- **Background:** `--panel` for chrome (header, side panels), `--ink` for the canvas and node cards' surrounding field; node card fill is `--panel` with a 1–2px border tinted to its namespace colour.
- **Shadow Strategy:** none at rest; see Elevation & Depth for the overlay/banner exceptions.
- **Border:** 1px `--line`, brightening to `--line-strong` on hover/active/selected.
- **Internal Padding:** 16px panel sections; node cards use the same horizontal 16px with vertical centering.

### Inputs / Fields
- **Style:** `--ink` background, 1px `--line` border, 3px radius, full width.
- **Focus:** the system-wide `:focus-visible` treatment — 2px `--signal` outline, 2px offset — never a glow or border-only cue, and never suppressed.

### Navigation (header)
Fixed 48px header: brand mark (a 12px ring, not a logo, sweeping only while `mode === "live" && !paused`), title, mode segmented control, window-length select, refresh/pause buttons, and a text-plus-dot connection status. Position is stable across all three modes; only the window-length select and pause button conditionally hide per mode.

### Icons (signature convention)
No Unicode or emoji glyphs anywhere in this build — every icon-like mark (checkmark, chevron) is an authored CSS border-technique shape at one consistent stroke weight (1.5px), replacing literal glyph characters that were present before this redesign. Treat this as the standing icon convention: new icons are drawn shapes, not characters from a font.

## Do's and Don'ts

### Do:
- **Do** scope the live/history accent swap through the single `.app[data-mode]` CSS override on `--signal`/`--signal-rgb` — never branch accent colour per component in JS.
- **Do** keep `--ok` (backend status) independent of `--signal` (temporal-mode chrome) even when their hex values happen to match today.
- **Do** run every new text/surface colour pairing through the contrast test in `frontend/tests/contrast.test.ts` before shipping it.
- **Do** draw new icon-like marks as authored CSS/SVG shapes at the established 1.5px stroke weight — not Unicode glyphs, not an icon font.
- **Do** keep motion to bounded, O(1)-per-graph-size elements, gated on `mode === "live" && !paused` — this is ADR-006 F9, not a style preference.
- **Do** carry namespace as the only colour-coded identity channel; kind stays a written label.

### Don't:
- **Don't** introduce a per-node or per-edge continuous animation at any graph size (ADR-006 F9 forbids it outright — the console's whole two-element motion budget respects this).
- **Don't** reintroduce a shape vocabulary for node kind. It was tried and deliberately removed before this redesign; kind stays text-only.
- **Don't** use a condensed or downloaded display face for labels. Tracked-out uppercase on the system sans stack is the entire device — there is no font-stretch fallback, because the no-web-fonts constraint can't reliably render one.
- **Don't** add decorative drop shadows to cards or panels; depth comes from the `--ink`/`--panel`/`--panel-high` tonal ramp, not elevation.
- **Don't** use Unicode/emoji glyphs for status, selection, or directional marks — draw them.
