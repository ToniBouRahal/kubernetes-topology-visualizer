# Prerequisites and Local Setup

Task ID: `P0-E5` · Governing ADR: [ADR-008](adr/ADR-008-testing-ci.md) D-8.3

Versions here are pinned in the `Makefile` and mirrored in `.github/workflows/ci.yml`. A local
upgrade must be mirrored in both, or CI and local behaviour diverge — the failure mode ADR-008
D-8.2 exists to prevent.

Run `make tools` to compare your machine against these pins.

## Pinned toolchain

| Tool | Pinned | Why this version |
|---|---|---|
| Go | **1.26.5** | `cilium/ebpf`, `bpf2go`, client-go informers |
| Node | **24.19.0** | Current active LTS (Krypton). Vite 7 requires ≥ 20.19; Node 18 is EOL. |
| Python | **3.13** | FastAPI + Pydantic v2 |
| Helm | **4.2.3** | **Helm 4, not 3.** Chart syntax and lint strictness are validated against v4. |
| kind | **0.32.0** | Three-node cluster for multi-node agent validation |
| kubectl | **1.31.1** | Cluster client |
| Docker | 28.5.2 | Image builds and the kind runtime |
| clang | 14 | BPF compilation happens in the pinned builder container, not with host clang |
| bpftool | 7.4.0 | `vmlinux.h` generation from BTF |
| golangci-lint | 2.12.2 | `make lint-go` |
| uv | 0.12.3 | Python environment management |

## Kernel requirements

The agent needs a Linux kernel with BTF and eBPF ring-buffer support.

| Requirement | Verified on this machine |
|---|---|
| Kernel ≥ 6.8 | 6.8.0-136-generic ✅ |
| BTF available | `/sys/kernel/btf/vmlinux` present ✅ |
| Ring buffer (`BPF_MAP_TYPE_RINGBUF`) | kernel ≥ 5.8 ✅ |

Check yours:

```bash
uname -r
test -r /sys/kernel/btf/vmlinux && echo "BTF OK" || echo "BTF MISSING"
```

Missing BTF is one of the failure modes that must produce an actionable message (ADR-001 §7
Phase 5, task `P5-T17`).

## Installation as performed on the development machine

Everything is **user-scoped**. Nothing was installed with `sudo`, and the system Python and system
Node were left untouched — `@openai/codex` and `@google/gemini-cli` are installed against the
system Node 18 and would break if it were replaced.

```bash
# Go → ~/.local/go, symlinked into ~/.local/bin
curl -fsSL -o go.tar.gz https://go.dev/dl/go1.26.5.linux-amd64.tar.gz
tar -C ~/.local -xzf go.tar.gz
ln -sf ~/.local/go/bin/go    ~/.local/bin/go
ln -sf ~/.local/go/bin/gofmt ~/.local/bin/gofmt

# Node → nvm, then symlinked into ~/.local/bin so non-interactive shells resolve it
nvm install 24 && nvm alias default 24
for b in node npm npx corepack; do
  ln -sf ~/.nvm/versions/node/v24.19.0/bin/$b ~/.local/bin/$b
done

# uv → ~/.local/bin (set the install dir explicitly; see the note below)
export UV_INSTALL_DIR="$HOME/.local/bin"
curl -LsSf https://astral.sh/uv/install.sh | sh

# Go-based tools
GOBIN="$HOME/.local/bin" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
GOBIN="$HOME/.local/bin" go install sigs.k8s.io/kind@latest
```

### Two environment traps worth knowing

**1. `~/.bashrc` returns early for non-interactive shells.** Ubuntu's default `.bashrc` begins with
a guard that exits immediately when the shell is not interactive, so the `nvm` lines near the end
never run under `make`, CI, or any tool-invoked shell. Node would silently resolve to the old
system version. The `~/.local/bin` symlinks above fix this because that directory precedes
`/usr/local/bin` on `PATH`.

**2. This machine runs VS Code as a snap**, which points `XDG_DATA_HOME` inside the snap directory.
Installers that honour XDG will land in `~/snap/code/<version>/.local/...`, which breaks on the
next VS Code update. Always pass an explicit install directory, as the `uv` command above does.

## Backend environment

```bash
make venv           # uv venv --python 3.13 && uv pip install -e ".[dev]"
```

## Verifying the setup

```bash
make tools          # local versions vs. the pins
make verify         # lint + test + contract drift check — what CI runs
```

Expected Phase 0 result: Go and Python lint and tests pass, the contract check passes, and the
frontend and Helm targets report that they are not yet scaffolded.

## Optional but recommended

| Tool | Use |
|---|---|
| `jq` | Inspecting `contracts/openapi.json` and API responses |
| `kubeconform` | Validating rendered Helm output in CI (`T-7.2`) |
| Chrome + Claude Code browser extension | Frontend debugging from Phase 2 (`P2-T5`, `P4-F14`) |

## Claude Code tooling

Plugins installed for this project (see [`docs/adr/README.md`](adr/README.md) §3):

```text
context7  gopls-lsp  pyright-lsp  typescript-lsp  frontend-design
playwright  hookify  skill-creator  github
```

Project skills live in `.claude/skills/` and load automatically. `claude-security` is installed at
Phase 5 only.
