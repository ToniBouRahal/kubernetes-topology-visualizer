#!/usr/bin/env python3
"""Export the FastAPI schema to contracts/openapi.json.

Output is byte-stable (sorted keys, fixed indent, trailing newline) so CI can detect drift with a
plain diff — test T-3.6. Run via `make contracts`; never hand-edit the output.

  --check   exit 1 if the committed file differs from what the app generates
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BACKEND = REPO_ROOT / "backend"
TARGET = REPO_ROOT / "contracts" / "openapi.json"

sys.path.insert(0, str(BACKEND))

from app.main import create_app  # noqa: E402


def render() -> str:
    schema = create_app().openapi()
    return json.dumps(schema, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def main() -> int:
    rendered = render()
    check = "--check" in sys.argv

    if check:
        if not TARGET.exists():
            print(f"FAIL: {TARGET.relative_to(REPO_ROOT)} does not exist. Run `make contracts`.")
            return 1
        if TARGET.read_text(encoding="utf-8") != rendered:
            print(
                f"FAIL: {TARGET.relative_to(REPO_ROOT)} is out of date.\n"
                "The committed contract does not match what the application generates.\n"
                "Run `make contracts` and commit the result."
            )
            return 1
        print(f"OK: {TARGET.relative_to(REPO_ROOT)} matches the application schema.")
        return 0

    TARGET.parent.mkdir(parents=True, exist_ok=True)
    TARGET.write_text(rendered, encoding="utf-8")
    print(f"wrote {TARGET.relative_to(REPO_ROOT)} ({len(rendered)} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
