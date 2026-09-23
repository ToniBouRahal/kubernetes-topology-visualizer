# ADR-010: The Graph Encodes Components and Their Connections, Not Kubernetes Kinds

- **Status:** Accepted for implementation
- **Date:** 2026-09-18
- **Parent:** ADR-001 §5.6 (graph readability) · ADR-006 (frontend topology UI)
- **Amends:** ADR-006 D-6.3 (visual encoding table), D-6.7 (the tested contrast baseline)
- **Component path:** `frontend/src/features/graph/`, `frontend/src/styles/`
- **Owning phase:** Post-Phase-5 redesign

## 1. Context

ADR-006 D-6.3 assigns every fact a non-colour cue, and gives node kind the strongest one available:
shape plus icon, with colour as the secondary channel. That table was written when a Service and the
Deployment behind it were two different nodes. Kind was load-bearing then — it was how a reader told
the routing indirection apart from the thing being routed to.

ADR-009 removed that distinction. A destination now resolves to the workload that serves it, and a
`Service` node appears only as an honest fallback when the backing workload cannot be determined. A
node is already the component. Its kind is the Kubernetes object that happens to run it.

The shape vocabulary did not survive contact with the graph either. Seven outlines had to be learned
from a legend before the picture could be read, so `encoding.ts` had already collapsed them to one
rounded outline and written the kind on each node in words instead — satisfying D-6.3's *intent*
(never colour alone) while abandoning its *letter* (shape plus icon). The result is the current node:
a card whose two lines of text are the component's name and the word `Deployment`.

That is the most valuable space on the canvas spent on the least-used fact. The question the tool
exists to answer is *what actually depends on what*. Kind answers none of it. Meanwhile the name —
the only thing a reader searches for, points at, and says out loud — is set in 12.5px beside a
coloured dot, and the graph has to be zoomed to read it at all.

There is a second, quieter cost. Because kind occupied the node's text, the namespace was left
carried by colour and a small monospace suffix, and node size carried nothing at all. The graph has
a spare ordinal channel it has never used.

## 2. Decision

**D-10.1 — Workload kind leaves the graph.** No node draws, writes, or is filtered by
`Deployment` / `StatefulSet` / `DaemonSet` / `Job` / `Pod` / `Service`. The D-6.3 row for node kind
is removed rather than re-encoded: a fact that is not shown needs no cue. `NodeKind` stays in the
wire contract, the database, and the generated clients untouched — this ADR changes what the graph
*draws*, not what the system *records*.

**D-10.2 — The component's name is drawn inside the node.** The node is a circle and its name sits
in the middle of it. Identity stops being a caption and becomes the node itself, readable without
zooming, hovering, or consulting a legend.

**D-10.3 — Node size carries degree.** A node's diameter grows with the number of distinct
components it was observed talking to. This is the spare ordinal channel from §1, and it is
non-colour, so D-6.3's property holds. Degree is computed from the observed graph in the selected
window — never from replica counts, resource requests, or any declared notion of importance.

Size and stroke width now carry two different quantities, and the legend must say so: **width is how
much** (successful establishments on one link), **size is how many** (distinct components on the
other end).

**D-10.4 — Namespace gains a written label.** Every node renders its namespace as text beneath the
circle, in addition to the colour fill. The `EXTERNAL` node reads `outside cluster`. This is what
keeps D-6.3's guarantee alive after D-10.1: with kind gone, namespace would otherwise have been left
as the one fact the graph carried by colour alone.

**D-10.5 — Direction keeps its arrowhead.** Unchanged from D-6.3, restated because the redesign
curves the edges and a curve is not self-evidently directed. An edge without an arrowhead in a
dependency graph is a worse defect than any this ADR fixes.

**D-10.6 — Selecting a component focuses its neighbourhood.** The selected node and the nodes it
links to stay at full strength; every other node drops back, as does every edge that does not touch
the selection — including an edge *between* two neighbours, which is not part of the selected
component's neighbourhood and must not be lit as though it were. Closing the details panel restores
the whole graph. This is a reading aid layered on top of the existing filters; it changes what is
emphasised, never what was observed.

**D-10.7 — The ground is light.** The console palette is replaced by a light cool-slate one. The
driver is the room: ADR-001 §6 fixes the demo at 1280x720 on a projector, where a near-black ground
with a single luminous accent loses most of its contrast range. D-6.7's WCAG AA requirement is
unchanged and `frontend/tests/contrast.test.ts` is re-baselined, not relaxed.

## 3. Consequences

**The graph answers its own question faster.** Name inside the node, one glance, no legend. This is
the entire point and everything below is a cost paid for it.

**You can no longer tell a StatefulSet from a Deployment in the UI.** Stated plainly because it is a
real loss: someone asking "is this workload stateful" must read the cluster, not this graph. The
field is still on the API response, so restoring it — most cheaply as a line in the details panel,
not as a cue on the node — needs only a decision, not a migration. Anyone who needs it should raise
its own ADR rather than reintroduce it quietly.

**Node size becomes a thing readers will over-read.** A large circle means *talks to many*, and some
readers will take it for *important* or *busy*. The legend states the rule, and size is deliberately
the weakest of the three quantitative cues, but the misreading is available and recording it here is
more useful than pretending the encoding is neutral.

**Nodes take more room, so the render ceiling gets closer.** A circle holding its own name is larger
than a 150x58 card, and `docs/limitations.md` §4.1 already puts the usable ceiling near 300 edges.
Nothing here raises the edge count, but the point at which the canvas *looks* crowded arrives
earlier. Namespace grouping, focus-on-selection (D-10.6), and the existing render budget are the
mitigations; if measurement shows the ceiling has moved materially, §4.1 gets re-measured, not
re-worded.

**The contrast baseline moves wholesale.** Every token changes, so the AA assertions are re-derived
against the new palette. A regression here is invisible to the eye of whoever made it and is exactly
what T-10.8 exists to catch.

**Two designs now exist in the history.** The near-black console styling is parked on
`design/passive-instrument` with `DESIGN.md` describing it. It is not dead code on `main` and does
not need maintaining; it is a recoverable alternative.

## 4. Alternatives rejected

**Keep kind, shrink it.** Setting the kind smaller or dimmer keeps a fact nobody uses and still
spends the node's second line on it. If it is not worth reading it is not worth drawing, and the
half-measure produces a node that is both cluttered and quiet.

**Move kind into an icon inside the circle, beside the name.** This is D-6.3's original instinct and
it fails the same way the shape vocabulary did: six icons to learn, and the one place they could sit
is the space the name now occupies. The legend cost returns in full.

**Size nodes by connection count rather than degree.** Traffic volume is already carried by edge
width, and encoding the same quantity twice makes a busy pair of components dominate the picture
twice over. Degree is the quantity the graph does not otherwise show.

**Highlight neighbours by colouring them rather than dimming the rest.** Any highlight colour would
collide with the namespace channel, which is the one channel D-10.4 just made load-bearing. Dimming
spends no hue at all.

**Keep the near-black ground and raise the accent's luminance for the projector.** Tried in the
parked design. Raising the accent to survive projection is what pushes a phosphor palette towards
the glare it was chosen to avoid, and it fixes nothing for the namespace hues, which must stay
distinguishable from each other on the same ground.

## 5. Tests

| ID | Assertion | Component |
|---|---|---|
| T-10.1 | No node renders a workload-kind string | frontend |
| T-10.2 | A node's accessible name is its component name and namespace, and states no kind | frontend |
| T-10.3 | Node diameter is a function of degree: equal degree renders equal diameter, greater degree renders greater diameter | frontend |
| T-10.4 | Every node renders its namespace as text; `EXTERNAL` renders `outside cluster` | frontend |
| T-10.5 | Every edge renders an arrowhead at its destination end | frontend |
| T-10.6 | Selecting a node dims every node that is neither it nor a direct neighbour | frontend |
| T-10.7 | Selecting a node dims every edge that does not touch it, including edges between two of its neighbours | frontend |
| T-10.8 | Every foreground/background pair in the light palette meets WCAG AA | frontend |

## 6. Tracker

- [x] **P6-F1** `tokens.css` re-based on the light cool-slate palette — D-10.7
- [x] **P6-F2** `encoding.ts`: kind encoding removed, namespace hue kept — D-10.1, D-10.4
- [x] **P6-F3** `TopologyNode`: circle, name inside, degree-scaled diameter, namespace label — D-10.2, D-10.3, D-10.4
- [x] **P6-F4** Degree computed per window and threaded to the node — D-10.3
- [x] **P6-F5** Curved edges keep a destination arrowhead — D-10.5
- [x] **P6-F6** Neighbour focus on selection, for nodes and edges — D-10.6
- [x] **P6-F7** `DetailsPanel` rows state source → destination, port, successful, failed, setup time — D-10.1
- [x] **P6-F8** `NodeList` drops the kind column, keeps namespace — D-10.1
- [x] **P6-F9** `contrast.test.ts` re-baselined; T-10.1 – T-10.8 — D-10.7
