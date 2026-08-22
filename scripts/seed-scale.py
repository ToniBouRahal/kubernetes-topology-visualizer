#!/usr/bin/env python3
"""Seed a synthetic graph at the size ADR-001 §6 states its latency target for.

The demo cluster produces ~13 nodes. Measuring p95 there and calling the 500-node / 2,000-edge
target met would be measuring the wrong thing, so this ingests a graph of the stated size through
the real ingest endpoint — same validation, same transaction, same storage path as an agent.

    python3 scripts/seed-scale.py --url http://localhost:18100 --nodes 500 --edges 2000
"""
import argparse
import json
import random
import string
import sys
import urllib.error
import urllib.request
# timezone.utc rather than datetime.UTC: this script runs on the host, where python may
# still be 3.10, and the alias only exists from 3.11.
from datetime import datetime, timedelta, timezone

CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
KINDS = ["Deployment", "StatefulSet", "DaemonSet", "Job", "Pod"]


def ulid() -> str:
    return "".join(random.choice(CROCKFORD) for _ in range(26))


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--cluster-id", default="kind-topology")
    ap.add_argument("--nodes", type=int, default=500)
    ap.add_argument("--edges", type=int, default=2000)
    ap.add_argument("--batch-size", type=int, default=200)
    args = ap.parse_args()

    rnd = random.Random(20260822)  # fixed seed: the same graph every run, so runs compare

    # Sources are workloads; targets are Services, matching how real resolution works — a source
    # never resolves to a Service (contracts/ids.md §6).
    workloads = [
        (f"scale-ns{i % 20}", KINDS[i % len(KINDS)], f"wl-{i}")
        for i in range(args.nodes // 2)
    ]
    services = [
        (f"scale-ns{i % 20}", "Service", f"svc-{i}")
        for i in range(args.nodes - len(workloads))
    ]

    def ref(ns, kind, name):
        return {"id": f"k8s:{args.cluster_id}:{ns}:{kind}:{name}",
                "kind": kind, "namespace": ns, "name": name}

    now = datetime.now(timezone.utc).replace(second=0, microsecond=0)
    first_seen = (now - timedelta(seconds=10)).isoformat().replace("+00:00", "Z")
    last_seen = now.isoformat().replace("+00:00", "Z")

    seen = set()
    edges = []
    while len(edges) < args.edges:
        s = rnd.choice(workloads)
        t = rnd.choice(services)
        port = rnd.choice([80, 443, 5432, 6379, 8080, 8443, 9000])
        key = (s[2], t[2], port)
        if key in seen:
            continue
        seen.add(key)
        edges.append({
            "source": ref(*s), "target": ref(*t),
            "protocol": "TCP", "destination_port": port,
            "connection_count": rnd.randint(1, 5000),
            "first_seen": first_seen, "last_seen": last_seen,
        })

    sent = 0
    for i in range(0, len(edges), args.batch_size):
        chunk = edges[i:i + args.batch_size]
        body = {
            "schema_version": 1,
            "cluster_id": args.cluster_id,
            "agent_id": "topology-agent/scale-seed",
            "batch_id": ulid(),
            "observed_at": last_seen,
            "interval_seconds": 10,
            "edges": chunk,
        }
        req = urllib.request.Request(
            f"{args.url}/api/v1/ingest/batches",
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                if r.status not in (200, 202):
                    print(f"unexpected status {r.status}", file=sys.stderr)
                    return 1
        except urllib.error.HTTPError as e:
            print(f"ingest failed {e.code}: {e.read()[:300].decode()}", file=sys.stderr)
            return 1
        sent += len(chunk)

    print(f"seeded {sent} edges across {args.nodes} nodes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
