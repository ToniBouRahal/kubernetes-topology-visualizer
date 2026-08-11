# Kubernetes Runtime Topology Visualizer
#
# CI calls these same targets (ADR-008 D-8.2). A CI job with its own inline command sequence
# drifts from local behaviour and eventually passes something a developer cannot reproduce.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# ── Pinned toolchain (ADR-008 D-8.3) ────────────────────────────────────────────────────────
# Keep in lockstep with docs/prerequisites.md and .github/workflows/ci.yml.
GO_VERSION      := 1.26.5
NODE_VERSION    := 24.19.0
PYTHON_VERSION  := 3.13
HELM_VERSION    := 4.2.3
KIND_VERSION    := 0.32.0
KUBECTL_VERSION := 1.31.1

REPO_ROOT   := $(shell pwd)
BACKEND     := $(REPO_ROOT)/backend
AGENT       := $(REPO_ROOT)/agent
FRONTEND    := $(REPO_ROOT)/frontend
CHART       := $(REPO_ROOT)/charts/topology-visualizer
KIND_VALUES := $(CHART)/ci/kind-values.yaml
VENV_PY     := $(BACKEND)/.venv/bin/python

RELEASE      := topology
KIND_CLUSTER := topology

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ── Aggregates ──────────────────────────────────────────────────────────────────────────────

.PHONY: verify
verify: lint test contracts-check ## Everything CI runs

.PHONY: lint
lint: lint-go lint-python lint-frontend lint-helm ## Lint every component

.PHONY: test
test: test-go test-python test-frontend ## Unit + integration tests (no cluster, no root)

.PHONY: test-unit
test-unit: test-go test-python ## Fast tests only — no containers, no cluster

# ── Go agent ────────────────────────────────────────────────────────────────────────────────

.PHONY: lint-go
lint-go: ## go vet + golangci-lint
	cd $(AGENT) && go vet ./...
	@command -v golangci-lint >/dev/null 2>&1 \
	  && (cd $(AGENT) && golangci-lint run ./...) \
	  || echo "  golangci-lint not on PATH — skipped (install: see docs/prerequisites.md)"

.PHONY: test-go
test-go: ## Agent unit tests
	cd $(AGENT) && go test ./... -count=1

.PHONY: generate
generate: ## Regenerate bpf2go bindings (requires the builder container; see ADR-002 §4)
	cd $(AGENT) && go generate ./...

.PHONY: test-ebpf
test-ebpf: ## Privileged eBPF tests — needs root and a 6.8+ kernel with BTF (ADR-008 D-8.1)
	@echo "Privileged tests are separated from ordinary CI but must run before release."
	@test -r /sys/kernel/btf/vmlinux || { echo "FAIL: /sys/kernel/btf/vmlinux not readable"; exit 1; }
	cd $(AGENT) && sudo -E $$(command -v go) test ./... -tags=privileged -count=1 -run 'Privileged'

# ── Python backend ──────────────────────────────────────────────────────────────────────────

.PHONY: venv
venv: ## Create the backend virtualenv
	cd $(BACKEND) && uv venv --python $(PYTHON_VERSION) && uv pip install -e ".[dev]"

.PHONY: lint-python
lint-python: ## ruff check + format check
	cd $(BACKEND) && $(VENV_PY) -m ruff check .
	cd $(BACKEND) && $(VENV_PY) -m ruff format --check .

.PHONY: test-python
test-python: ## Backend unit + contract tests
	cd $(BACKEND) && $(VENV_PY) -m pytest -q

# ── Contracts (ADR-003) ─────────────────────────────────────────────────────────────────────

.PHONY: contracts
contracts: ## Regenerate contracts/openapi.json from the FastAPI app
	$(VENV_PY) scripts/export_openapi.py

.PHONY: contracts-check
contracts-check: ## Fail if the committed contract drifts from the app (T-3.6)
	$(VENV_PY) scripts/export_openapi.py --check

# ── Frontend ────────────────────────────────────────────────────────────────────────────────

.PHONY: lint-frontend
lint-frontend: ## Typecheck + lint (no-op until Phase 2)
	@test -f $(FRONTEND)/package.json \
	  && (cd $(FRONTEND) && npm run lint --if-present && npm run typecheck --if-present) \
	  || echo "  frontend not scaffolded yet — Phase 2 (P2-F1)"

.PHONY: test-frontend
test-frontend: ## Frontend unit/component tests (no-op until Phase 2)
	@test -f $(FRONTEND)/package.json \
	  && (cd $(FRONTEND) && npm test --if-present) \
	  || echo "  frontend not scaffolded yet — Phase 2 (P2-F1)"

# ── Helm / Kubernetes (ADR-007) ─────────────────────────────────────────────────────────────

.PHONY: lint-helm
lint-helm: ## helm lint + render + RBAC/schema assertions (T-7.1 – T-7.4)
	@if [ -f $(CHART)/Chart.yaml ]; then \
	  bash scripts/verify-chart.sh; \
	else \
	  echo "  chart not scaffolded yet — P0-K3"; \
	fi

.PHONY: chart-template
chart-template: ## Render the chart with kind values
	helm template $(RELEASE) $(CHART) -f $(KIND_VALUES)

.PHONY: kind-up
kind-up: ## Create the three-node kind cluster
	kind create cluster --name $(KIND_CLUSTER) --config kind/cluster.yaml

.PHONY: kind-down
kind-down: ## Delete ONLY this project's kind cluster
	kind delete cluster --name $(KIND_CLUSTER)

# ── Demo loop — implemented in Phase 5 (ADR-007 D-7.6) ──────────────────────────────────────

.PHONY: demo-up demo-traffic demo-change demo-verify demo-down
demo-up demo-traffic demo-change demo-verify demo-down:
	@echo "$@ is a Phase 5 deliverable (P5-K11). See docs/IMPLEMENTATION-PLAN.md."
	@exit 1

# ── Tooling report ──────────────────────────────────────────────────────────────────────────

.PHONY: tools
tools: ## Print the local toolchain against the pinned versions
	@printf "%-14s %-14s %s\n" TOOL PINNED LOCAL
	@printf "%-14s %-14s %s\n" go        $(GO_VERSION)      "$$(go version 2>/dev/null | awk '{print $$3}' | sed 's/go//')"
	@printf "%-14s %-14s %s\n" node      $(NODE_VERSION)    "$$(node -v 2>/dev/null | sed 's/v//')"
	@printf "%-14s %-14s %s\n" python    $(PYTHON_VERSION)  "$$($(VENV_PY) -V 2>/dev/null | awk '{print $$2}')"
	@printf "%-14s %-14s %s\n" helm      $(HELM_VERSION)    "$$(helm version --short 2>/dev/null | sed 's/^v//;s/+.*//')"
	@printf "%-14s %-14s %s\n" kind      $(KIND_VERSION)    "$$(kind version 2>/dev/null | awk '{print $$2}' | sed 's/v//')"
	@printf "%-14s %-14s %s\n" kubectl   $(KUBECTL_VERSION) "$$(kubectl version --client -o json 2>/dev/null | jq -r .clientVersion.gitVersion | sed 's/v//')"
