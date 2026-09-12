# Demo script

A fifteen-minute walkthrough that shows what the system does and, just as deliberately, what it
does not.

Every command is real and every claim is checkable while you watch. Nothing here is staged — the
graph is whatever the cluster actually did.

**Before you start:** `make preflight` (checks kernel, BTF and inotify limits), Docker running,
~6 GiB free. Total runtime from nothing: about ten minutes, most of it image builds.

---

## 1 · Bring it up (≈8 min, unattended)

```bash
make demo-up
```

Creates a three-node kind cluster, builds and side-loads three images, installs the chart with
PostgreSQL, and applies the demo workloads across two namespaces.

**The point to make while it runs:** the demo workloads are ordinary `nginx`, `redis` and `busybox`
containers. Nothing is instrumented, no sidecar is injected, and no application knows the system
exists. Show `demo/demo-workloads.yaml` — there is nothing in it about topology.

## 2 · Prove there is nothing up the sleeve (1 min)

```bash
kubectl --context kind-topology get pods -n demo -n data
kubectl --context kind-topology get ds -n topology
```

One agent per node, including the control plane. The agent is the only privileged pod:

```bash
kubectl --context kind-topology get pods -n topology -o json \
  | jq -r '.items[] | select(.spec.containers[].securityContext.privileged==true) | .metadata.name'
```

Exactly one name. Everything else runs non-root with a read-only root filesystem and no capabilities.

## 3 · Generate a known amount of traffic (1 min)

```bash
make demo-traffic
```

Opens **exactly 100 connections** to each of two destinations, then waits for aggregation.

```bash
make demo-verify
```

The line worth pausing on:

```
== the counted burst is reported exactly ==
  PASS demo-traffic -> redis:   opened 100, reported 100
  PASS demo-traffic -> backend: opened 100, reported 100
```

That is the claim: not "traffic appears" but "the number of connections opened is the number
reported". It is checkable, and it is checked.

## 4 · Look at it (3 min)

```bash
kubectl --context kind-topology -n topology port-forward svc/topology-visualizer-frontend 8080:8080
```

Open `http://localhost:8080`.

Four things to point out, in order:

1. **`frontend → backend` and `backend → redis` are there** — and they were never declared anywhere.
   The system found them by watching the kernel.
2. **Two frontend replicas and two backend replicas produce one node each.** Identity is the
   workload, not the pod. Delete a pod and the graph does not change shape.
3. **`backend → EXTERNAL`** — traffic leaving the cluster is summarised to a single node. There is
   deliberately no per-IP node, and no external address is stored anywhere.
4. **The legend says `connection count — TCP establishments, not requests`.** Say this out loud. A
   service handling ten thousand requests over one pooled connection reports 1.

Delete a pod while the graph is on screen:

```bash
kubectl --context kind-topology delete pod -n demo -l app=frontend --wait=false
```

The node stays. The graph does not fragment into per-pod entries.

## 5 · Show a change appearing (3 min)

```bash
make demo-change
```

Adds a `payment` service and a `reporter` workload that talks to it — a new dependency that no
manifest of the existing services mentions.

In the UI, switch to **Compare** and compare the last 5 minutes against the previous 5. The new
edge is labelled `NEW` in words, not only in colour — the whole comparison is readable in greyscale.

Switching the first dropdown to **Two points in time** compares any two chosen moments instead of
two adjacent windows — "this morning against yesterday morning". Both periods take the same length,
because connection counts are totals and not rates: an unequal pair would make the longer period
win every edge and read as a system-wide increase. Worth saying out loud if asked why the length is
not per-period.

**The limit to state here, not hide:** history reaches back only as far as the backend's retention
window — `backend.retentionHours`, **1440 (two months)** by default. A period older than that
returns an empty comparison, because the data was deleted rather than never recorded.

```bash
make demo-verify
```

```
  PASS reporter -> payment TCP:6380 (443)
```

**The argument this supports:** a manifest review would not have caught this dependency appearing.
Runtime observation did, within seconds, without touching the application.

## 6 · Show the limits (3 min)

This section is not optional. A demo that only shows the good case is not evidence.

**Connections are not requests.** Already said in step 4 — repeat it here with the legend on screen.

**A dependency that did not communicate does not exist.** Narrow the window to 1 minute; edges that
were real a moment ago disappear. That is correct behaviour and the direct cost of the property
that makes the tool useful.

**Byte volume is not reported.** It was measured, found exact but only readable at connection close,
and *declined* — 8 persistent connections carried 32 MB and reported nothing. Showing bytes would
have drawn the busiest edges as the faintest. See `docs/evaluation/byte-accounting.md`.

**The interface does not scale to its own stated ceiling.** Past ~300 edges it stops rendering.
Measured, documented in `limitations.md` §4.1, not fixed.

Being able to say all four of these, with numbers, is the point of the evaluation work.

## 7 · Tear down (1 min)

```bash
make demo-down
```

Removes the release, the demo namespaces (selected by the label this project sets, never by bare
name), and the kind cluster — and nothing else.

---

## If something goes wrong mid-demo

| symptom | say this, then |
|---|---|
| graph is empty | "aggregation is on a ten-second cycle" — wait 20 s, widen the window to 15 m |
| a pod is restarting | `kubectl -n topology get pods`; the backend exits if the database is unreachable at startup, by design |
| everything broke at once | check `kubectl get pods -A` for `<invalid>` ages — a laptop that suspended jumps the clock and invalidates ServiceAccount tokens; see `troubleshooting.md` |

The fallback that always works: `make demo-verify` prints the current state of every claim in this
script as pass or fail, without needing the UI.
