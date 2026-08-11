---
name: k8s-demo-loop
description: >
  Build, deploy, and verify the topology visualizer in kind. Load before creating or resetting the
  cluster, building or loading images, installing or upgrading the Helm release, generating demo
  traffic, or diagnosing a pod that will not start.
---

# kind demo loop

ADR: `docs/adr/ADR-007-packaging-deployment.md`. Tools: Helm **4.2.3**, kind 0.32.0, kubectl 1.31.1.

## 1. The canonical loop

Matches the `Makefile` exactly. If you find yourself typing something not in this list, the
Makefile is wrong and should be fixed — Phase 5 requires the demo to succeed from committed
instructions alone.

```bash
make kind-up          # kind create cluster --name topology --config kind/cluster.yaml
make images           # docker build agent, backend, frontend
                      # kind load docker-image ... --name topology
make chart-lint       # helm lint + helm template against ci/kind-values.yaml
make demo-up          # helm upgrade --install + rollout status for each workload
make demo-traffic     # generate the known demo traffic
make demo-verify      # assert the expected edges via the API
make demo-down        # uninstall + delete this project's cluster only
```

## 2. The image-reload gotcha

**`kind load docker-image` with an unchanged tag does not restart running pods.** The new image
sits in the node's store while the old container keeps running, and you debug code that is not
deployed.

Either use a content-based tag, or `kubectl rollout restart` after loading. This wastes more time
than any other mistake in this loop.

## 3. Teardown scope

`make demo-down` removes **only** this project's Helm release and the `topology` kind cluster.

Never `kubectl delete ns` on a namespace the chart did not create. Never `kind delete cluster`
without `--name topology`. Deleting an unrelated cluster during a teardown is unrecoverable.

## 4. Diagnostic ladder

| Symptom | Check first |
|---|---|
| Agent pod Pending | `kubectl describe pod` — control-plane toleration missing? |
| Agent CrashLoopBackOff | BTF (`/sys/kernel/btf/vmlinux`) and host mounts, **before** suspecting code |
| Backend not Ready | migrations, then database reachability |
| Frontend blank | port-forward target and `CORS_ALLOWED_ORIGINS` |
| **Graph empty but agent logs show edges** | **`CLUSTER_ID` mismatch between agent and backend.** Check this before suspecting the collector — it is the most common cause and produces no error anywhere. |
| Edges appear then vanish | retention window vs. the query window |

## 5. Verification, not impression

"It works" must be an assertion:

```bash
kubectl port-forward svc/topology-backend 8000:8000 &
curl -s 'http://localhost:8000/api/v1/graph?window=5m' | jq -r \
  '.edges[] | "\(.source_id) -> \(.target_id) :\(.destination_port) x\(.connection_count)"' | sort
```

Compare against the expected demo edge list in `demo/`. `make demo-verify` automates exactly this.

## 6. Helm 4, not Helm 3

The installed binary is Helm **4.2.3**. Verify chart apiVersion, subchart handling, and lint
strictness against Helm 4 documentation rather than Helm 3 habit. CI pins the same version; a local
upgrade must be mirrored in `.github/workflows/ci.yml` and the `Makefile`.

## 7. Cluster hygiene

- The kind cluster is named `topology`. Always pass `--name topology`.
- `kind get clusters` before creating, so an existing cluster is reused or deliberately replaced.
- The agent must run on **every** node including the control-plane — `kubectl get pods -o wide`
  and count. Phase 1 and Phase 5 both gate on this.

## 8. Before you finish

- [ ] Did images actually reload, or are pods still running the old tag?
- [ ] Is an agent pod Running on all three nodes?
- [ ] Does `CLUSTER_ID` match on both sides?
- [ ] Did you verify edges via the API rather than by looking at logs?
- [ ] Does teardown touch only this project's resources?
