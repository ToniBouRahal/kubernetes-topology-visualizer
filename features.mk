# End-to-end feature tests and the larger "shop" demo mesh. Included by the Makefile.
#
#   make test-features          every feature against a live kind cluster (~15 min, creates it if absent)
#   make test-everything        `make verify` (lint, unit, contracts) and then the above
#   make mesh-up                ~30 microservices in five namespaces, ~40 pods
#   make mesh-up MESH_SCALE=100 the same plus 100 tenant services, for load
#
# Each ft-* target is one phase and can be run alone, e.g. `make ft-anomalies`. The assertions
# live in scripts/feature-test.sh, which reads the running system the way demo-verify.sh does.
# Everything here uses $(KUBECTL)/$(HELM_K), pinned to the kind context like the rest of the file.

MESH_SCALE ?= 0
FT_ENV := KIND_CONTEXT=$(KIND_CONTEXT) NAMESPACE=$(NAMESPACE) RELEASE=$(RELEASE) MESH_SCALE=$(MESH_SCALE)

# The order matters: diff and anomalies need the mesh's traffic to be a few minutes old, metrics
# reads the counters the anomaly phase moves, and the CLI and scale phases read the busiest graph.
FT_PHASES := ft-base ft-mesh ft-security ft-auth ft-diff ft-anomalies ft-metrics ft-alerts ft-cli ft-scale

# ── The mesh ────────────────────────────────────────────────────────────────────────────────

.PHONY: mesh-up
mesh-up: ## Deploy the ~30-service shop mesh (MESH_SCALE=N adds N tenant services)
	python3 scripts/gen-mesh.py --scale $(MESH_SCALE) | $(KUBECTL) apply -f - >/dev/null
	@for ns in $$(python3 scripts/gen-mesh.py --scale $(MESH_SCALE) --namespaces); do \
	  echo "waiting for $$ns"; \
	  for w in $$($(KUBECTL) -n $$ns get deploy,sts,ds -o name); do \
	    $(KUBECTL) -n $$ns rollout status $$w --timeout=300s >/dev/null || exit 1; done; \
	done
	@# Pods settle 15s before their first connection (see gen-mesh.py), then the agent's flush.
	@echo "mesh is up: $$($(KUBECTL) get pods -A -l topology-demo=true --no-headers | wc -l) demo pods; waiting $(DEMO_SETTLE)s + 15s for the first edges"
	@sleep $$(( $(DEMO_SETTLE) + 15 ))

.PHONY: mesh-scale
mesh-scale: ## The mesh plus 100 tenant services (override with MESH_SCALE=N)
	$(MAKE) mesh-up MESH_SCALE=$(if $(filter 0,$(MESH_SCALE)),100,$(MESH_SCALE))

.PHONY: mesh-down
mesh-down: ## Remove the mesh (namespaces labelled topology-mesh=true), leaving the base demo
	-$(KUBECTL) delete namespace -l topology-mesh=true --ignore-not-found --timeout=3m

.PHONY: mesh-status
mesh-status: ## Pods per mesh namespace and what the kind nodes are using
	@$(KUBECTL) get pods -A -l topology-demo=true --no-headers | awk '{c[$$1]++; if ($$4 != "Running" && $$4 != "Completed") b[$$1]++} \
	  END {for (n in c) printf "  %-14s %3d pods  %s\n", n, c[n], (b[n] ? b[n] " not running" : "")}' | sort
	@docker stats --no-stream --format '  {{.Name}}  cpu {{.CPUPerc}}  mem {{.MemUsage}}' $$(kind get nodes --name $(KIND_CLUSTER))

# ── Convenience for the manual half ─────────────────────────────────────────────────────────

.PHONY: ui
ui: ## Port-forward the UI to http://localhost:8080 and print the demo sign-in
	@echo "sign in as admin@topology.local, password: $$($(KUBECTL) -n $(NAMESPACE) get secret $(RELEASE)-visualizer-auth -o jsonpath='{.data.demo-password}' | base64 -d)"
	$(KUBECTL) -n $(NAMESPACE) port-forward svc/$(RELEASE)-visualizer-frontend 8080:8080

.PHONY: cli-login
cli-login: $(if $(wildcard bin/kubectl-topology),,cli) ## Sign the CLI in through the browser (once), so ft-cli can run
	@echo "demo password: $$($(KUBECTL) -n $(NAMESPACE) get secret $(RELEASE)-visualizer-auth -o jsonpath='{.data.demo-password}' | base64 -d)"
	@set -e; \
	if ! curl -s -o /dev/null --max-time 2 http://localhost:8080/; then \
	  $(KUBECTL) -n $(NAMESPACE) port-forward svc/$(RELEASE)-visualizer-frontend 8080:8080 >/dev/null 2>&1 & \
	  pf=$$!; trap "kill $$pf" EXIT; sleep 3; \
	fi; \
	bin/kubectl-topology login

# ── Phases ──────────────────────────────────────────────────────────────────────────────────

.PHONY: ft-base
ft-base: ## Phase: agent on every node, counted burst, demo edges (T-7.5, T-7.10, T-7.11)
	$(MAKE) agent-verify
	$(MAKE) demo-traffic
	$(MAKE) demo-verify

.PHONY: ft-mesh
ft-mesh: ## Phase: every mesh edge, workload kinds, replica collapse, filters, node/edge detail
	@$(FT_ENV) bash scripts/feature-test.sh mesh

.PHONY: ft-diff
ft-diff: ## Phase: remove one service, add another, /diff says REMOVED and NEW (~5 min)
	@$(FT_ENV) bash scripts/feature-test.sh diff

.PHONY: ft-anomalies
ft-anomalies: ## Phase: trigger all five anomaly rules at once and assert each (~4 min)
	@$(FT_ENV) bash scripts/feature-test.sh anomalies

.PHONY: ft-metrics
ft-metrics: ## Phase: Prometheus series for anomalies, canaries, ingest, alerts
	@$(FT_ENV) bash scripts/feature-test.sh metrics

.PHONY: ft-security
ft-security: ## Phase: mTLS-only API, plain listener, Pod Security labels, then verify-tls
	@$(FT_ENV) bash scripts/feature-test.sh security
	$(MAKE) verify-tls

.PHONY: ft-auth
ft-auth: ## Phase: anonymous and forged requests refused, Keycloak discovery served
	@$(FT_ENV) bash scripts/feature-test.sh auth

.PHONY: ft-cli
ft-cli: ## Phase: every CLI command, Mermaid export, MCP tools (needs `make cli` and `make cli-login` once)
	@$(FT_ENV) bash scripts/feature-test.sh cli

.PHONY: ft-alerts
ft-alerts: ## Phase: chat test message (needs `make alerts-on` first, otherwise skipped)
	@$(FT_ENV) bash scripts/feature-test.sh alerts

.PHONY: ft-scale
ft-scale: ## Phase: graph size and API latency at the current mesh size
	@$(FT_ENV) bash scripts/feature-test.sh scale

# ── Aggregates ──────────────────────────────────────────────────────────────────────────────

.PHONY: test-features
test-features: ## Every feature end to end on kind: cluster (if absent), mesh, all phases, summary
	@if $(HELM_K) status $(RELEASE) -n $(NAMESPACE) >/dev/null 2>&1; then \
	  echo "release '$(RELEASE)' is installed — reusing it"; \
	else \
	  $(MAKE) demo-up; \
	fi
	$(MAKE) mesh-up
	@# Every phase runs even when an earlier one fails, so one run reports everything; the
	@# target still fails at the end if anything did.
	@failed=""; start=$$(date +%s); \
	for phase in $(FT_PHASES); do \
	  echo; echo "━━━━━━━━━━ $$phase ━━━━━━━━━━"; \
	  $(MAKE) --no-print-directory $$phase || failed="$$failed $$phase"; \
	done; \
	echo; echo "━━━━━━━━━━ summary ($$(( ($$(date +%s) - start) / 60 )) min) ━━━━━━━━━━"; \
	for phase in $(FT_PHASES); do \
	  case " $$failed " in *" $$phase "*) printf '  \033[31mFAIL\033[0m %s\n' $$phase ;; \
	                       *) printf '  \033[32mPASS\033[0m %s\n' $$phase ;; esac; \
	done; \
	echo; echo "Still to check by eye: make ui, then grouping, search, compare mode, Export diagram,"; \
	echo "the anomaly panel and the Metrics links (with make demo-observability)."; \
	test -z "$$failed"

.PHONY: test-everything
test-everything: verify test-features ## Lint, unit and contract tests, then every feature on kind
