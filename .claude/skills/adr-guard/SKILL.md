---
name: adr-guard
description: >
  Enforce ADR-001 scope boundaries. Load before implementing any feature not explicitly listed in
  an ADR's decisions, and before changing an architectural choice (transport, storage engine, event
  source, identity grammar, deployment model).
---

# ADR scope guard

Authority order: `docs/FYP_Project_Scope_Source_of_Truth.md` > `ADR-001` > `ADR-002`–`ADR-008`.
A component ADR may add detail; it may never widen scope.

## 1. Explicitly out of scope — ADR-001 §4.2

Stop and cite the section if a request matches any of these:

- packet payload capture or deep packet inspection
- HTTP paths, headers, status codes, TLS contents, DNS-derived domain attribution
- UDP, SCTP, ICMP, non-IP Unix socket traffic
- multi-cluster aggregation
- distributed tracing, request latency
- automatic network-policy generation or enforcement
- hosted SaaS, billing, multi-tenancy
- high-availability backend replicas
- Windows nodes
- **IPv6** in the initial release
- Prometheus or Loki as *mandatory* dependencies

## 2. Deferred — ADR-001 §13, each needs its own ADR first

HA PostgreSQL · multi-cluster identity · IPv6 capture · UDP/DNS visibility · SSE or WebSocket
updates · NetworkPolicy recommendations · authentication and role-based UI access · storage beyond
retention · ML/prediction/anomaly detection · optional Prometheus and Loki panels ·
capability-based agent hardening.

## 3. Source-of-truth non-goals — §25

Not a Rancher, a service mesh, a CNI, a Prometheus/Grafana replacement, an APM, distributed
tracing, log management, DPI, payload capture, full L7 parsing, automatic remediation, a SIEM,
network-policy enforcement, or Internet traffic analysis.

## 4. Procedure when a change is genuinely needed

If the change contradicts a decision in ADR-002–ADR-008:

1. **Write a new numbered ADR in `docs/adr/` first** — context, decision, consequences
   (ADR-001 §12 instruction 6).
2. Update the affected component ADR's tracker and `docs/IMPLEMENTATION-PLAN.md`.
3. Only then implement.

Never make an architectural change and document it afterwards. The ADR is the decision record; a
retroactive one records nothing.

## 5. The smallest-implementation rule

ADR-001 §12 instruction 5: prefer the smallest implementation that satisfies the ADR. Do not add
out-of-scope features. "While I'm here" is how a fixed-scope FYP misses its deadline.

## 6. Phase discipline

ADR-001 §12 instruction 4: implement phases in order; stop advancing when a phase's acceptance
criteria fail. Do not start the next phase's deliverables to compensate for a blocked one — see the
`phase-gate` skill.

## 7. What is legitimately in scope

Before blocking something, check it is not simply *unfinished*: the full accepted scope is
ADR-001 §4.1 and the phase deliverables in §7. Byte-level traffic volume, historical comparison,
retention, Helm packaging, and multi-node validation are all **in** scope and required.
