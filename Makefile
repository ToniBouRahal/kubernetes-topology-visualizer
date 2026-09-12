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

# Agent flush (10s) + delivery + UI poll headroom. Kept on its own line: a trailing comment on a
# `:=` assignment leaves the whitespace in the value, which then appears in log output as "25   s".
DEMO_SETTLE  := 25
RELEASE      := topology

# The chart names this secret `<fullname>-db` (templates/secrets.yaml). This used to read
# `$(RELEASE)-visualizer-database`, a name the chart never creates, so the "reuse the existing
# secret" branch below could never be taken: every `demo-up` against a surviving cluster minted a
# NEW password while PostgreSQL still had the old one persisted in its PVC, and the backend then
# failed to authenticate against its own database.
DB_SECRET    := $(RELEASE)-visualizer-db

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

.PHONY: spike-bytes
spike-bytes: ## Byte-accounting experiment (P4-A22) via sudo — see spike-bytes-docker if sudo is interactive
	@echo "Byte-accounting spike (ADR-002 D-2.8). This answers a question; it may conclude 'infeasible'."
	@test -r /sys/kernel/btf/vmlinux || { echo "FAIL: /sys/kernel/btf/vmlinux not readable"; exit 1; }
	cd $(AGENT) && sudo -E $$(command -v go) test ./internal/spike/... -tags=privileged -count=1 -v -run 'Privileged'

.PHONY: spike-bytes-docker
spike-bytes-docker: bpf-builder ## Same experiment in a privileged container — no interactive sudo needed
	@# The recorded results in docs/evaluation/byte-accounting.md came from THIS target. tracefs
	@# must be bind-mounted or the tracepoint cannot be attached from inside the container
	@# ("neither debugfs nor tracefs are mounted"), which looks like a kernel-support failure and
	@# is not one.
	@test -r /sys/kernel/btf/vmlinux || { echo "FAIL: /sys/kernel/btf/vmlinux not readable"; exit 1; }
	docker run --rm --privileged --network=host \
	  -v /sys/kernel/btf:/sys/kernel/btf:ro \
	  -v /sys/fs/bpf:/sys/fs/bpf \
	  -v /sys/kernel/debug:/sys/kernel/debug \
	  -v /sys/kernel/tracing:/sys/kernel/tracing \
	  -v "$(PWD)/agent":/build -w /build \
	  -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
	  $(BPF_BUILDER) \
	  go test ./internal/spike/... -tags=privileged -count=1 -v -timeout 10m -run 'Privileged'

# ── Python backend ──────────────────────────────────────────────────────────────────────────

.PHONY: venv
venv: ## Create the backend virtualenv
	cd $(BACKEND) && uv venv --python $(PYTHON_VERSION) && uv pip install -e ".[dev]"

# Every target below needs the virtualenv. Without this guard a clean checkout gets
# "make: *** [contracts-check] Error 127" — the shell's "command not found", which says nothing
# about what to do. ADR-001 §7 asks for actionable failures; that applies to the build too.
.PHONY: require-venv
require-venv:
	@test -x $(VENV_PY) || { \
	  echo "The backend virtualenv is missing: $(VENV_PY)"; \
	  echo "Create it with:  make venv"; \
	  exit 1; }

.PHONY: lint-python
lint-python: require-venv ## ruff check + format check
	cd $(BACKEND) && $(VENV_PY) -m ruff check .
	cd $(BACKEND) && $(VENV_PY) -m ruff format --check .

.PHONY: test-python
test-python: require-venv ## Backend unit + contract tests (PostgreSQL half skips without a database)
	cd $(BACKEND) && $(VENV_PY) -m pytest -q

TEST_PG_DSN ?= postgresql://postgres:test@localhost:5433/topology

.PHONY: test-db
test-db: ## Run the contract suite against a real PostgreSQL (T-5.12)
	@docker inspect topology-test-pg >/dev/null 2>&1 || \
	  docker run --rm -d --name topology-test-pg \
	    -e POSTGRES_PASSWORD=test -e POSTGRES_DB=topology -p 5433:5432 postgres:17-alpine >/dev/null
	@until docker exec topology-test-pg pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
	cd $(BACKEND) && TEST_DATABASE_URL=$(TEST_PG_DSN) $(VENV_PY) -m pytest tests/contract -q

.PHONY: test-db-down
test-db-down: ## Stop the test database
	-docker rm -f topology-test-pg

# ── Contracts (ADR-003) ─────────────────────────────────────────────────────────────────────

.PHONY: contracts
contracts: require-venv ## Regenerate contracts/openapi.json from the FastAPI app
	$(VENV_PY) scripts/export_openapi.py

.PHONY: contracts-check
contracts-check: require-venv ## Fail if the committed contract drifts from the app (T-3.6)
	$(VENV_PY) scripts/export_openapi.py --check

# ── Frontend ────────────────────────────────────────────────────────────────────────────────

# `cmd || echo "not scaffolded"` was the shape here, and it swallowed failures: a real lint or
# test error took the `||` branch, printed a reassuring message, and exited 0. The frontend has
# existed since Phase 2, so the guard now asserts rather than excuses — and `--if-present` is gone
# too, since a renamed script should fail loudly, not skip silently.

.PHONY: lint-frontend
lint-frontend: ## Typecheck + lint
	@test -f $(FRONTEND)/package.json || { echo "FAIL: $(FRONTEND)/package.json missing"; exit 1; }
	cd $(FRONTEND) && npm run lint && npm run typecheck

.PHONY: test-frontend
test-frontend: ## Frontend unit/component tests
	@test -f $(FRONTEND)/package.json || { echo "FAIL: $(FRONTEND)/package.json missing"; exit 1; }
	cd $(FRONTEND) && npm test

.PHONY: image-frontend
image-frontend: ## Build the frontend image and side-load it into kind
	docker build -t topology-frontend:dev $(FRONTEND)
	kind load docker-image topology-frontend:dev --name $(KIND_CLUSTER)

.PHONY: image-backend
image-backend: ## Build the backend image and side-load it into kind
	docker build -t topology-backend:dev backend
	kind load docker-image topology-backend:dev --name $(KIND_CLUSTER)

# ── Helm / Kubernetes (ADR-007) ─────────────────────────────────────────────────────────────

.PHONY: verify-privacy
verify-privacy: ## Screenshots, log defaults and committed credentials (P5-T18, ADR-008 D-8.7)
	@bash scripts/verify-privacy.sh

.PHONY: experiments
experiments: ## Measure every ADR-001 §6 performance target and report met/missed (P5-T12)
	@bash scripts/experiments.sh all

.PHONY: seed-scale
seed-scale: ## Ingest a synthetic 500-node graph for scale measurement (needs a port-forward on 18100)
	@echo "Ingests through the real endpoint. Remove afterwards — see phase-5.md."
	python3 scripts/seed-scale.py --url http://localhost:18100 --nodes 500 --edges 2000

.PHONY: verify-pinning
verify-pinning: ## Assert every third-party image is pinned by digest, not just a tag (P5-K10)
	@bash scripts/verify-image-pinning.sh

.PHONY: scan-images
scan-images: ## Scan the built images for HIGH/CRITICAL vulnerabilities (ADR-008 D-8.7)
	@# Trivy runs in a container so nothing has to be installed on the host. The cache is kept in
	@# the repo-local .trivy-cache so repeated runs do not re-download a 100 MB database.
	@mkdir -p .trivy-cache
	@for img in topology-agent:dev topology-backend:dev topology-frontend:dev postgres:17-alpine; do \
	  echo "== $$img =="; \
	  docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
	    -v "$(REPO_ROOT)/.trivy-cache":/root/.cache/ aquasec/trivy:latest image \
	    --scanners vuln --severity HIGH,CRITICAL --quiet "$$img" || true; \
	done

.PHONY: verify-db-image
verify-db-image: ## Prove the database image PULLS rather than relying on a side-loaded copy (P3-K5, ADR-007 D-7.2)
	@# The cluster's own database pod is not evidence: `kind load` side-loads images, and a
	@# side-loaded image reports an imageID of `import-<date>@sha256:...` with no Pull event, so it
	@# would start happily on a machine that could never fetch it. imagePullPolicy: Always forces
	@# the registry path that a clean machine would take.
	@#
	@# The proof is the pod REACHING Succeeded under Always: that policy makes the kubelet contact
	@# the registry, so an unreachable or renamed image fails with ErrImagePull instead. The event
	@# below is printed for the timing, not relied on — events from an earlier run of this target
	@# linger under the same pod name and would happily match a stale grep.
	@set -e; \
	image=$$(helm template $(RELEASE) $(CHART) -f $(KIND_VALUES) \
	    --set postgresql.enabled=true --set postgresql.auth.password=throwaway 2>/dev/null \
	  | awk '/^kind: StatefulSet$$/,/^---$$/' | awk '/image:/{gsub(/"/,"",$$2); print $$2; exit}'); \
	test -n "$$image" || { echo "FAIL: could not determine the database image from the chart"; exit 1; }; \
	echo "database image from the chart: $$image"; \
	$(KUBECTL) delete pod db-image-pull-check -n $(NAMESPACE) --ignore-not-found --now >/dev/null 2>&1 || true; \
	$(KUBECTL) run db-image-pull-check -n $(NAMESPACE) \
	  --image="$$image" --image-pull-policy=Always --restart=Never \
	  --command -- postgres --version >/dev/null; \
	$(KUBECTL) wait --for=jsonpath='{.status.phase}'=Succeeded \
	  pod/db-image-pull-check -n $(NAMESPACE) --timeout=180s >/dev/null; \
	echo -n "  reported version: "; $(KUBECTL) logs db-image-pull-check -n $(NAMESPACE); \
	echo -n "  most recent pull: "; \
	$(KUBECTL) get events -n $(NAMESPACE) \
	  --field-selector involvedObject.name=db-image-pull-check \
	  -o custom-columns=MSG:.message --no-headers | grep -E "^Successfully pulled" | tail -1; \
	$(KUBECTL) delete pod db-image-pull-check -n $(NAMESPACE) --now >/dev/null 2>&1; \
	echo "  PASS: the database image pulls from the registry"

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

# ── Demo loop (ADR-007 D-7.6) ───────────────────────────────────────────────────────────────
#
# `make demo-up` must work on a clean supported machine with no manifest hand-editing. Every
# target here pins the kind context and scopes every deletion by release, cluster or label — see
# demo-down for why that matters.

.PHONY: images
images: ## Build all three images and side-load them into kind
	docker build -t topology-agent:dev $(AGENT)
	docker build -t topology-backend:dev backend
	docker build -t topology-frontend:dev $(FRONTEND)
	kind load docker-image topology-agent:dev topology-backend:dev topology-frontend:dev \
	  --name $(KIND_CLUSTER)

.PHONY: demo-up
demo-up: ## Cluster + images + install + demo workloads, ready to observe
	@if kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)"; then \
	  echo "kind cluster '$(KIND_CLUSTER)' already exists — reusing it"; \
	else \
	  $(MAKE) kind-up; \
	fi
	$(MAKE) images
	@# A password is required by values.schema.json when the in-cluster database is enabled. It is
	@# generated per install and never committed; the chart puts it in a Secret (ADR-005 D-5.7).
	@set -e; \
	if $(KUBECTL) get secret $(DB_SECRET) -n $(NAMESPACE) >/dev/null 2>&1; then \
	  echo "reusing the existing database secret"; \
	  PW=$$($(KUBECTL) get secret $(DB_SECRET) -n $(NAMESPACE) \
	        -o jsonpath='{.data.password}' | base64 -d); \
	else \
	  PW=$$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 24); \
	  echo "generated a new database password (stored only in the cluster Secret)"; \
	fi; \
	$(HELM_K) upgrade --install $(RELEASE) $(CHART) \
	  --namespace $(NAMESPACE) --create-namespace \
	  -f $(KIND_VALUES) \
	  --set postgresql.enabled=true --set postgresql.auth.password="$$PW" \
	  --wait --timeout 6m
	$(MAKE) demo-workloads
	@echo
	@echo "Ready. Watch the topology build up with:"
	@echo "    make demo-traffic && make demo-verify"
	@echo "    $(KUBECTL) -n $(NAMESPACE) port-forward svc/$(RELEASE)-visualizer-frontend 8080:8080"

.PHONY: demo-traffic
demo-traffic: ## Generate a known, counted burst of traffic and wait for it to be aggregated
	-$(KUBECTL) delete job demo-traffic -n demo --ignore-not-found --now
	$(KUBECTL) apply -f demo/demo-traffic.yaml
	@# The job settles for 25s before opening anything (see demo-traffic.yaml), so this waits well
	@# past that rather than timing out on a job that is working correctly.
	$(KUBECTL) wait --for=condition=complete job/demo-traffic -n demo --timeout=300s
	@$(KUBECTL) logs job/demo-traffic -n demo | tail -2
	@# The agent aggregates on a 10s flush; without this wait, demo-verify races the pipeline and
	@# reports a missing edge that arrives a second later.
	@echo "waiting $(DEMO_SETTLE)s for aggregation and delivery..."
	@sleep $(DEMO_SETTLE)

.PHONY: demo-change
demo-change: ## Apply the controlled topology change (a new dependency appears)
	$(KUBECTL) apply -f demo/demo-change.yaml
	$(KUBECTL) -n data rollout status deploy/payment --timeout=180s
	$(KUBECTL) -n demo rollout status deploy/reporter --timeout=180s
	@echo "waiting $(DEMO_SETTLE)s for the new edge to be observed..."
	@sleep $(DEMO_SETTLE)

.PHONY: demo-verify
demo-verify: ## Assert the expected edges through the API (T-7.10, T-7.11)
	@KIND_CONTEXT=$(KIND_CONTEXT) NAMESPACE=$(NAMESPACE) RELEASE=$(RELEASE) \
	  bash scripts/demo-verify.sh

.PHONY: demo-down
demo-down: ## Remove ONLY what this project created
	@# Surgical by construction (D-7.6). Deleting a developer's unrelated cluster during a demo
	@# teardown is unrecoverable, so nothing here takes a wildcard:
	@#   - the release is removed by name, in this project's namespace
	@#   - demo namespaces are selected by the topology-demo label this project sets, never by
	@#     bare name, so a pre-existing `demo` namespace belonging to someone else is untouched
	@#   - the kind cluster is deleted by name, and only if it exists
	-$(HELM_K) uninstall $(RELEASE) --namespace $(NAMESPACE) --wait --timeout 3m
	-$(KUBECTL) delete namespace -l topology-demo=true --ignore-not-found --timeout=3m
	-$(KUBECTL) delete namespace $(NAMESPACE) --ignore-not-found --timeout=3m
	@if kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)"; then \
	  kind delete cluster --name $(KIND_CLUSTER); \
	else \
	  echo "no kind cluster named '$(KIND_CLUSTER)' — nothing to delete"; \
	fi

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
