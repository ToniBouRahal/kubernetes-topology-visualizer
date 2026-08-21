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
NAMESPACE   := topology
VENV_PY     := $(BACKEND)/.venv/bin/python

RELEASE      := topology
KIND_CLUSTER := topology
KIND_CONTEXT := kind-$(KIND_CLUSTER)

# Every cluster command pins the kind context explicitly.
#
# kubectl's *current* context on a developer machine is very often a real remote cluster — this
# project's own development machine defaults to an EKS cluster. An unqualified `kubectl apply`
# or `kubectl delete` would land there. Pinning the context makes that impossible by
# construction rather than by remembering (ADR-007 D-7.6).
KUBECTL := kubectl --context $(KIND_CONTEXT)
HELM_K  := helm --kube-context $(KIND_CONTEXT)

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
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  cd $(AGENT) && golangci-lint run ./...; \
	else \
	  echo "  golangci-lint not on PATH — skipped (install: see docs/prerequisites.md)"; \
	fi

.PHONY: test-go
test-go: ## Agent unit tests
	cd $(AGENT) && go test ./... -count=1

BPF_BUILDER := topology-bpf-builder:$(GO_VERSION)

.PHONY: bpf-builder
bpf-builder: ## Build the pinned container used for BPF compilation
	docker build -t $(BPF_BUILDER) -f $(AGENT)/build/Dockerfile.bpf-builder $(AGENT)/build

.PHONY: generate
generate: bpf-builder ## Regenerate bpf2go bindings inside the builder container (ADR-002 §4)
	docker run --rm \
	  --user $$(id -u):$$(id -g) \
	  -v $(AGENT):/build \
	  -w /build/internal/collector \
	  $(BPF_BUILDER) \
	  go generate ./...

.PHONY: vmlinux
vmlinux: ## Regenerate bpf/vmlinux.h from this kernel's BTF (committed; regenerate deliberately)
	@test -r /sys/kernel/btf/vmlinux || { echo "FAIL: /sys/kernel/btf/vmlinux not readable"; exit 1; }
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $(AGENT)/bpf/vmlinux.h
	@echo "wrote $(AGENT)/bpf/vmlinux.h ($$(wc -l < $(AGENT)/bpf/vmlinux.h) lines)"

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

.PHONY: preflight
preflight: ## Check host prerequisites before creating a cluster
	@bash scripts/preflight.sh

.PHONY: kind-up
kind-up: preflight ## Create the three-node kind cluster
	kind create cluster --name $(KIND_CLUSTER) --config kind/cluster.yaml
	$(KUBECTL) get nodes

.PHONY: kind-down
kind-down: ## Delete ONLY this project's kind cluster
	kind delete cluster --name $(KIND_CLUSTER)

.PHONY: kind-context
kind-context: ## Show which cluster the project targets vs. kubectl's current context
	@echo "project targets : $(KIND_CONTEXT)"
	@echo "kubectl current : $$(kubectl config current-context 2>/dev/null || echo none)"
	@echo "(the project never uses the current context — every command pins $(KIND_CONTEXT))"

.PHONY: agent-image
agent-image: ## Build the agent image and side-load it into kind
	docker build -t topology-agent:dev $(AGENT)
	kind load docker-image topology-agent:dev --name $(KIND_CLUSTER)
	# An unchanged tag does not restart running pods; force a rollout so the new image is used.
	-$(KUBECTL) -n $(NAMESPACE) rollout restart ds/topology-visualizer-agent 2>/dev/null

.PHONY: agent-deploy
agent-deploy: ## Install/upgrade the agent-only release into kind (Phase 1)
	$(HELM_K) upgrade --install $(RELEASE) $(CHART) \
	  --namespace $(NAMESPACE) --create-namespace \
	  -f $(KIND_VALUES) \
	  --set backend.enabled=false --set frontend.enabled=false
	$(KUBECTL) -n $(NAMESPACE) rollout status ds/topology-visualizer-agent --timeout=180s

.PHONY: agent-verify
agent-verify: ## Assert an agent pod is Running on EVERY node (T-7.5)
	@bash scripts/verify-agent-coverage.sh

.PHONY: demo-workloads
demo-workloads: ## Apply the demo topology (two namespaces, unmodified workloads)
	$(KUBECTL) apply -f demo/demo-workloads.yaml
	$(KUBECTL) -n demo rollout status deploy/frontend --timeout=180s
	$(KUBECTL) -n demo rollout status deploy/backend --timeout=180s
	$(KUBECTL) -n data rollout status statefulset/redis --timeout=180s

.PHONY: agent-edges
agent-edges: ## Print the service-level edges the agents currently report
	@bash scripts/show-edges.sh

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
