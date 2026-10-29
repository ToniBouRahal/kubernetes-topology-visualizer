#!/usr/bin/env python3
"""Generate the "shop" demo mesh: ~30 unmodified microservices across five namespaces.

The base demo (demo/demo-workloads.yaml) proves capture with four workloads. This one gives the
graph something a reviewer would recognise as a real system — an edge tier, a dozen business
services, an async tier, five data stores, a DaemonSet and a Job — so grouping, filtering,
search, diff and the anomaly rules are exercised at a size where they matter.

Every service is the same pinned redis image: `redis-server` is the listener (any TCP port works
for an active-open collector) and BusyBox `nc` opens one new connection per dependency per loop.
Service ports differ (8080, 9090, 5432, 9092, …) so the graph's edge labels read like a real
system even though every pod listens on 6379.

    python3 scripts/gen-mesh.py                 # manifests on stdout
    python3 scripts/gen-mesh.py --scale 100     # plus 100 tenant services, for load
    python3 scripts/gen-mesh.py --expected      # the edges the graph must show, one per line

Stdlib only, like the other scripts the Makefile calls.
"""

from __future__ import annotations

import argparse
import sys
from dataclasses import dataclass, field

IMAGE = "redis:7-alpine@sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf"
INTERVAL = 5
# Payment provider and mail relay stand-ins. Absent on an offline machine, which the verifier
# reports as a note, never a failure — the same rule demo-verify applies to its external edge.
EXTERNAL_HOST = "example.com"


@dataclass
class Svc:
    ns: str
    name: str
    port: int
    deps: list[str] = field(default_factory=list)  # "ns/name" or "EXTERNAL:port"
    kind: str = "Deployment"
    replicas: int = 1


# ── The shop ────────────────────────────────────────────────────────────────────────────────
# Namespaces are prefixed so nothing collides with a developer's own `ops` or `data`.
E, S, A, D, O = "shop-edge", "shop", "shop-async", "shop-data", "shop-ops"

MESH: list[Svc] = [
    # edge tier — synthetic-users is the "internet" knocking on the gateway
    Svc(E, "synthetic-users", 0, [f"{E}/ingress-gateway"]),
    Svc(E, "ingress-gateway", 443, [f"{E}/web-ui", f"{E}/mobile-bff", f"{S}/auth"], replicas=2),
    Svc(E, "web-ui", 8080, [f"{S}/catalog", f"{S}/search", f"{S}/cart", f"{S}/user-profile",
                            f"{S}/recommendation"], replicas=2),
    Svc(E, "mobile-bff", 8080, [f"{S}/catalog", f"{S}/cart", f"{S}/checkout", f"{S}/user-profile"]),
    # business services
    Svc(S, "auth", 9090, [f"{D}/postgres", f"{D}/redis-cache"]),
    Svc(S, "user-profile", 8080, [f"{D}/postgres", f"{D}/redis-cache", f"{S}/auth"]),
    Svc(S, "catalog", 8080, [f"{D}/mongodb", f"{D}/redis-cache", f"{S}/pricing", f"{S}/reviews"],
        replicas=2),
    Svc(S, "search", 8080, [f"{D}/elasticsearch", f"{S}/catalog"]),
    Svc(S, "recommendation", 8080, [f"{S}/catalog", f"{A}/kafka", f"{D}/redis-cache"]),
    Svc(S, "reviews", 8080, [f"{D}/mongodb"]),
    Svc(S, "pricing", 9090, [f"{D}/redis-cache", f"{S}/promotions"]),
    Svc(S, "promotions", 8080, [f"{D}/postgres"]),
    Svc(S, "cart", 8080, [f"{D}/redis-cache", f"{S}/pricing", f"{S}/inventory"], replicas=2),
    Svc(S, "checkout", 8080, [f"{S}/cart", f"{S}/inventory", f"{S}/pricing",
                              f"{S}/payment-service", f"{S}/shipping", f"{A}/kafka", f"{S}/auth"],
        replicas=2),
    Svc(S, "inventory", 9090, [f"{D}/postgres"]),
    Svc(S, "payment-service", 8080, [f"{D}/postgres", f"{A}/fraud-detector", "EXTERNAL:443"]),
    Svc(S, "shipping", 8080, [f"{D}/postgres", f"{A}/kafka"]),
    # async tier — consumers with no callers, as a real event-driven tier has
    Svc(A, "kafka", 9092, kind="StatefulSet"),
    Svc(A, "order-processor", 0, [f"{A}/kafka", f"{D}/postgres", f"{S}/inventory",
                                  f"{A}/notification"], replicas=2),
    Svc(A, "notification", 8080, [f"{A}/kafka", f"{A}/email-worker"]),
    Svc(A, "email-worker", 8080, ["EXTERNAL:443"]),
    Svc(A, "fraud-detector", 8080, [f"{D}/redis-cache", f"{D}/mongodb"]),
    Svc(A, "analytics-ingest", 0, [f"{A}/kafka", f"{D}/object-store"]),
    # data stores
    Svc(D, "postgres", 5432, kind="StatefulSet"),
    Svc(D, "redis-cache", 6379, kind="StatefulSet"),
    Svc(D, "mongodb", 27017, kind="StatefulSet"),
    Svc(D, "elasticsearch", 9200, kind="StatefulSet"),
    Svc(D, "object-store", 9000, kind="StatefulSet"),
    # ops — one pod per node collapsing to one node, and a long-running batch Job. Not a CronJob:
    # a CronJob's Job is its own identity (resolver.go workloadFor; CronJob is not one of the six
    # contract kinds), so every run would add another node and clutter the graph by the minute.
    Svc(O, "log-shipper", 0, [f"{A}/kafka"], kind="DaemonSet"),
    Svc(O, "reindex-batch", 0, [f"{D}/postgres", f"{D}/object-store"], kind="Job"),
]

TENANT_NS = "shop-tenants"
TENANT_TARGETS = [f"{S}/catalog", f"{S}/cart", f"{S}/search", f"{S}/auth", f"{S}/user-profile",
                  f"{D}/redis-cache", f"{D}/postgres", f"{A}/kafka"]


def tenants(n: int) -> list[Svc]:
    """N extra services, each calling two shop services and the previous tenant.

    Deterministic (no RNG), so --expected and the cluster agree on every run.
    """
    out = []
    for i in range(1, n + 1):
        deps = [TENANT_TARGETS[i % len(TENANT_TARGETS)],
                TENANT_TARGETS[(i * 3 + 1) % len(TENANT_TARGETS)]]
        if i > 1:
            deps.append(f"{TENANT_NS}/tenant-{i - 1:03d}")
        out.append(Svc(TENANT_NS, f"tenant-{i:03d}", 8080, sorted(set(deps))))
    return out


def by_ref(services: list[Svc]) -> dict[str, Svc]:
    return {f"{s.ns}/{s.name}": s for s in services}


def loop_script(svc: Svc, index: dict[str, Svc], listen: bool) -> str:
    lines = []
    if listen:
        lines.append("redis-server --port 6379 --save '' --appendonly no --daemonize yes >/dev/null")
    # Settle before the first connection, for the reason demo-traffic.yaml gives: a pod's first
    # opens can precede the agent learning its address and would be dropped as unresolved.
    lines.append("sleep 15")
    lines.append("while true; do")
    for dep in svc.deps:
        if dep.startswith("EXTERNAL:"):
            host, port = EXTERNAL_HOST, dep.split(":", 1)[1]
        else:
            target = index[dep]
            host, port = f"{target.name}.{target.ns}.svc.cluster.local", str(target.port)
        lines.append(f"  nc -w 2 {host} {port} </dev/null >/dev/null 2>&1 || true")
    lines.append(f"  sleep {INTERVAL}")
    lines.append("done")
    return "\n".join(lines)


def container(svc: Svc, index: dict[str, Svc]) -> str:
    listen = svc.port != 0
    if svc.deps:
        cmd = ["/bin/sh", "-c"]
        args = [loop_script(svc, index, listen)]
        body = (f"          command: {fmt_list(cmd)}\n"
                f"          args:\n            - |\n"
                + "\n".join(f"              {ln}" for ln in args[0].splitlines()) + "\n")
    else:
        body = "          args: [\"--save\", \"\", \"--appendonly\", \"no\"]\n"
    return (
        f"        - name: {svc.name}\n"
        f"          image: {IMAGE}\n"
        f"{body}"
        + ("          ports:\n            - {name: tcp, containerPort: 6379}\n" if listen else "")
        + "          resources:\n"
          "            requests: {cpu: 10m, memory: 16Mi}\n"
          "            limits: {memory: 64Mi}\n"
    )


def fmt_list(items: list[str]) -> str:
    return "[" + ", ".join(f'"{i}"' for i in items) + "]"


def workload(svc: Svc, index: dict[str, Svc]) -> str:
    labels = f"{{app: {svc.name}}}"
    pod_spec = (
        f"    metadata:\n      labels: {{app: {svc.name}, topology-demo: \"true\"}}\n"
        f"    spec:\n"
        + ("      restartPolicy: Never\n" if svc.kind == "Job" else "")
        + "      containers:\n"
        + container(svc, index)
    )
    head = f"apiVersion: {'batch/v1' if svc.kind == 'Job' else 'apps/v1'}\nkind: {svc.kind}\n" \
           f"metadata:\n  name: {svc.name}\n  namespace: {svc.ns}\n  labels: {labels}\n"
    if svc.kind == "Job":
        # A Job's template is immutable, so re-applying the mesh leaves a running one alone.
        return head + "spec:\n  backoffLimit: 0\n  template:\n" + pod_spec
    spec = "spec:\n"
    if svc.kind != "DaemonSet":
        spec += f"  replicas: {svc.replicas}\n"
    if svc.kind == "StatefulSet":
        spec += f"  serviceName: {svc.name}\n"
    spec += f"  selector:\n    matchLabels: {labels}\n  template:\n"
    return head + spec + pod_spec


def service(svc: Svc) -> str:
    return (f"apiVersion: v1\nkind: Service\nmetadata:\n  name: {svc.name}\n  namespace: {svc.ns}\n"
            f"spec:\n  selector: {{app: {svc.name}}}\n"
            f"  ports:\n    - {{name: tcp, port: {svc.port}, targetPort: 6379}}\n")


def namespace(ns: str) -> str:
    # topology-demo=true is what `make demo-down` and `make mesh-down` select on.
    return f"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: {ns}\n" \
           f"  labels: {{topology-demo: \"true\", topology-mesh: \"true\"}}\n"


def manifests(services: list[Svc]) -> str:
    index = by_ref(services)
    docs = [namespace(ns) for ns in dict.fromkeys(s.ns for s in services)]
    for s in services:
        docs.append(workload(s, index))
        if s.port:
            docs.append(service(s))
    return "# Generated by scripts/gen-mesh.py — edit the generator, not this output.\n---\n" \
        + "---\n".join(docs)


def expected(services: list[Svc]) -> str:
    """One edge per line: src_ns src_kind src_name dst_ns dst_kind dst_name port strength.

    strength is `hard` (must be present) or `soft` (reported, not asserted): external edges need
    outbound network, which an offline machine does not have.
    """
    index = by_ref(services)
    rows = []
    for s in services:
        for dep in s.deps:
            if dep.startswith("EXTERNAL:"):
                rows.append((s.ns, s.kind, s.name, "-", "External", "EXTERNAL",
                             dep.split(":", 1)[1], "soft"))
            else:
                t = index[dep]
                rows.append((s.ns, s.kind, s.name, t.ns, t.kind, t.name, str(t.port), "hard"))
    return "\n".join(" ".join(r) for r in rows) + "\n"


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--scale", type=int, default=0, help="extra tenant services (default 0)")
    p.add_argument("--expected", action="store_true", help="print expected edges, not manifests")
    p.add_argument("--namespaces", action="store_true", help="print the namespaces, one per line")
    a = p.parse_args()
    if a.scale < 0 or a.scale > 999:
        p.error("--scale must be 0..999")
    services = MESH + tenants(a.scale)
    if a.namespaces:
        sys.stdout.write("\n".join(dict.fromkeys(s.ns for s in services)) + "\n")
    elif a.expected:
        sys.stdout.write(expected(services))
    else:
        sys.stdout.write(manifests(services))
    return 0


if __name__ == "__main__":
    sys.exit(main())
