#!/usr/bin/env python3
"""Assert that CLUSTER_ID resolves to exactly one value across rendered manifests.

D-7.5: CLUSTER_ID is embedded in every canonical node ID. If the agent and the backend disagree,
the graph is silently empty — no error is logged anywhere, which is why this is asserted rather
than trusted. Reads rendered YAML from a file path or stdin.
"""

from __future__ import annotations

import sys

import yaml


def collect(stream) -> set[str]:
    found: set[str] = set()
    for doc in yaml.safe_load_all(stream):
        if not isinstance(doc, dict):
            continue

        data = doc.get("data")
        if isinstance(data, dict) and "CLUSTER_ID" in data:
            found.add(str(data["CLUSTER_ID"]))

        spec = doc.get("spec")
        if not isinstance(spec, dict):
            continue
        pod_spec = (spec.get("template") or {}).get("spec") or {}
        for container in pod_spec.get("containers") or []:
            for env in container.get("env") or []:
                if env.get("name") == "CLUSTER_ID" and "value" in env:
                    found.add(str(env["value"]))
    return found


def main() -> int:
    if len(sys.argv) > 1:
        with open(sys.argv[1], encoding="utf-8") as fh:
            values = collect(fh)
    else:
        values = collect(sys.stdin)

    if len(values) == 1:
        print(f"OK single CLUSTER_ID: {next(iter(values))}")
        return 0
    if not values:
        print("FAIL no CLUSTER_ID found in rendered manifests")
        return 1
    print(f"FAIL CLUSTER_ID resolves to {len(values)} distinct values: {sorted(values)}")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
