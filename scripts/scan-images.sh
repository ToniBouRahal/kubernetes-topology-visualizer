#!/usr/bin/env bash
# Vulnerability scan and SBOM for the three built images and the database image (ADR-014 D-14.11).
#
#   bash scripts/scan-images.sh [tag] [sbom-dir]      default tag: dev (what `make images` builds)
#
# Fails on HIGH or CRITICAL findings that have a fix available. Unfixed findings are reported but
# do not fail: there is nothing to upgrade to, and a gate that cannot be satisfied gets disabled.
# Every image also gets a CycloneDX SBOM, so what shipped can be checked against advisories
# published after the build.
#
# Trivy runs from a digest-pinned image rather than an installed binary, so a developer and CI
# run the identical scanner. It reads the images through the Docker socket.
set -uo pipefail

TAG="${1:-dev}"
SBOM_DIR="$(mkdir -p "${2:-sbom}" && cd "${2:-sbom}" && pwd)"
TRIVY_IMAGE="aquasec/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969"
# The vulnerability database is cached between runs; downloading it is most of a cold scan.
CACHE_DIR="${TRIVY_CACHE_DIR:-$HOME/.cache/trivy}"
mkdir -p "$CACHE_DIR"

trivy() {
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    -v "$CACHE_DIR:/root/.cache/trivy" \
    -v "$SBOM_DIR:/sbom" \
    "$TRIVY_IMAGE" "$@"
}

# The database image is third-party but ships in the release all the same. Read from the chart's
# own values, so the scan covers exactly the digest the chart deploys.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_IMAGE="$(python3 -c '
import sys, yaml
i = yaml.safe_load(open(sys.argv[1]))["postgresql"]["image"]
ref = i["repository"] + ":" + str(i["tag"])
print(ref + "@" + i["digest"] if i.get("digest") else ref)
' "$REPO_ROOT/charts/topology-visualizer/values.yaml")"

failed=()
for entry in "agent=topology-agent:$TAG" "backend=topology-backend:$TAG" "frontend=topology-frontend:$TAG" "postgresql=$DB_IMAGE"; do
  name="${entry%%=*}"
  image="${entry#*=}"
  echo "== $image =="
  if ! docker image inspect "$image" >/dev/null 2>&1 && ! docker pull -q "$image" >/dev/null; then
    echo "$image is not available — run make images (or pass the tag it was built with)" >&2
    failed+=("$name")
    continue
  fi
  # One exclusion, from the gate only (the SBOM still lists it): the database image's gosu. The
  # image's entrypoint runs gosu only `if [ "$(id -u)" = '0' ]`, to drop from root to postgres; the
  # chart starts PostgreSQL as uid 999 with runAsNonRoot, so this binary never executes. Its
  # findings are all in the Go runtime it was built with (1.24.6). limitations.md §6.8.
  skip=()
  [[ "$name" == postgresql ]] && skip=(--skip-files usr/local/bin/gosu)
  trivy image --quiet --scanners vuln --format cyclonedx --output "/sbom/$name.cdx.json" "$image"
  if ! trivy image --quiet --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 "${skip[@]}" "$image"; then
    failed+=("$name")
  fi
done

echo "SBOMs: $SBOM_DIR"
if ((${#failed[@]})); then
  echo "image scan FAILED: ${failed[*]}" >&2
  exit 1
fi
echo "image scan: no fixable HIGH or CRITICAL vulnerabilities"
