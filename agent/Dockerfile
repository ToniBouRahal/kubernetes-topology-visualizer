# Two-stage build for the topology agent.
#
# The BPF object and its Go bindings are committed (see agent/internal/collector/gen.go), so this
# build does not need clang or libbpf — it compiles the embedded object straight into the binary.
# Regenerating them is a separate, explicit step: `make generate`, which uses the pinned builder
# in build/Dockerfile.bpf-builder.

FROM golang:1.26-bookworm AS builder

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The cilium/ebpf loader is pure Go, so the binary can be fully static. A static binary is what
# lets the runtime stage stay minimal.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/agent ./cmd/agent

# ── Runtime ─────────────────────────────────────────────────────────────────────────────────
# Debian slim rather than distroless or scratch: troubleshooting a privileged eBPF DaemonSet on
# a node genuinely benefits from a shell being available, and this container is already
# privileged, so removing the shell buys very little. Documented as a deliberate trade in
# docs/limitations.md.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/agent /usr/local/bin/agent

# Runs as root and privileged: loading and attaching eBPF programs requires host capabilities on
# the target setup. Accepted for the FYP (ADR-001 §5.7); capability reduction is attempted only
# after the system is correct, and must not block the demonstration.
USER root

EXPOSE 8081 9090

ENTRYPOINT ["/usr/local/bin/agent"]
