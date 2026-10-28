# ADR-018: Anomaly and Canary Alerts to Slack, Microsoft Teams and Telegram

- **Status:** Accepted for implementation
- **Date:** 2026-10-29
- **Parent:** ADR-016 (the findings this ADR delivers) · ADR-001 §6 (privacy: what may leave the
  cluster) · §4.2 (no new mandatory dependency)
- **Amends:** ADR-016 §1 and §3 ("nothing calls out of the cluster") and §4 (webhooks rejected):
  for this one feature, off by default, outbound notification is now allowed. ADR-004 (one
  background dispatcher). ADR-007 D-7.5 (`alerts.*` values, one optional Secret).
- **Component path:** `backend/app/alerts/`, `backend/app/main.py`, `backend/app/api/routes.py`,
  `backend/app/settings.py`, `charts/topology-visualizer/`, `Makefile`
- **Owning phase:** Post-Phase-5 extension

## 1. Context

ADR-016 finds anomalies and canary contacts, but it leaves the finding *in* the cluster: an API
response, a WARNING log line and a Prometheus gauge. That is correct for an operator with
Alertmanager already wired to a pager. It leaves out a common case: a small team, or a single
cluster with no alerting pipeline, that wants a message on a phone when a canary is touched. In
the FYP demo, touching a canary and having a phone buzz on stage shows the point of a canary
faster than a log line can.

ADR-016 §4 rejected webhooks for two reasons. Both still hold, and this ADR keeps each in a
narrower form:

1. **"The topology does not leave the cluster."** Alerts send a small part of it out: one or a few
   relationships, named, with ports and counts. That is a real change to ADR-001 §6's posture, so
   it is **off by default**, sends only to destinations the operator configures, and is stated
   in the values file, the operator guide and `limitations.md`.
2. **"Alertmanager does routing, silencing and deduplication better."** It does, and this ADR
   does not compete with it. The gauges and log lines stay as they are. There is no routing
   tree, no silencing and no escalation here: one message per finding, to every configured
   channel.

**Scope check (adr-guard).** Not in ADR-001 §4.2. Not in §13 (anomaly detection was taken into
scope by ADR-016; this ADR only adds delivery). Not one of the source-of-truth §25 non-goals:
it does no remediation, it is not a SIEM (no log collection, correlation or retention), and it
does no Internet traffic analysis.

## 2. Decision

**D-18.1 — Three channels, each optional, all off by default.** `alerts.enabled` (default
`false`) is the master switch. Under it, each channel is active when its credentials are present:

| Channel | Credential (Secret key) | Non-secret value | Request |
|---|---|---|---|
| Slack | `slack-webhook-url` | — | `POST` incoming-webhook URL, `{"text": …}` |
| Microsoft Teams | `teams-webhook-url` | — | `POST` Workflows webhook URL, a `message` with one Adaptive Card 1.4 attachment |
| Telegram | `telegram-bot-token` | `alerts.telegram.chatId` | `POST https://api.telegram.org/bot<token>/sendMessage`, `{"chat_id", "text"}` |

Teams uses the **Workflows** webhook ("Post to a channel when a webhook request is received"),
since Office 365 connector webhooks were retired in May 2026. Only `https://` URLs are accepted.
A `hooks.slack.com` host is required for Slack. Teams Workflows hosts vary by tenant
(`*.logic.azure.com`, `*.powerplatform.com`), so the Teams host is not pinned. The Telegram host
is fixed in code.

Enabled with no channel configured is a start-up error, not a silent no-op. An operator who
switched alerts on and receives nothing should find out at deploy time, not during an incident.

**D-18.2 — Credentials are a Secret, never a ConfigMap, never a log line.** A webhook URL *is* a
credential: anyone holding a Slack or Teams webhook URL can post to that channel. The chart
either creates `<release>-alerts` from `alerts.slack.webhookUrl`, `alerts.teams.webhookUrl` and
`alerts.telegram.botToken` (supplied with `--set`, never committed), or uses
`alerts.existingSecret` with the three keys above. Each key is optional. The backend reads them as
`ALERT_SLACK_WEBHOOK_URL`, `ALERT_TEAMS_WEBHOOK_URL` and `ALERT_TELEGRAM_BOT_TOKEN`. A log line
names the channel and the HTTP status only, never the URL, the token or an exception message
that could contain either. Redirects are not followed: a `3xx` is a failure.

**D-18.3 — Two triggers, split by type so nothing is sent twice.**

- **Canary contact is sent at ingest** (ADR-016 D-16.5's hook), because a canary is only worth
  having if the alert arrives while the intruder is still there. The ingest request does not wait
  for it. It puts the finding on the dispatcher's queue (D-18.5) and returns. A repeat for the
  same `(source, canary, port)` is suppressed for `alerts.canaryCooldownSeconds` (default 300),
  so a scanner hitting a canary 500 times sends one message, not 500.
- **Every other type is sent by the evaluator** (ADR-016 D-16.3), on the pass where its anomaly
  id first becomes active, the same moment the WARNING line is logged now. `CANARY_CONTACT` is
  excluded from this path, since ingest already sent it.

The evaluator's **first pass after start-up sends nothing**. It only records what is active.
Without this, every rolling update re-announces every standing anomaly. The cost: an anomaly that
first appears while the backend is restarting is never sent (it is still in the API, the log and
the gauge). Canary contact is not affected, because it is sent at ingest.

**D-18.4 — A severity floor, one message per pass.** `alerts.minSeverity` is `critical`
(default) or `warning`. At the default, `NEW_EDGE` and `SLOW_SETUP` are never sent, because on a
busy cluster they are routine. The findings one evaluator pass sends to a channel go in **one
message**: up to 5 listed, then "and N more", so a deploy that creates 40 new edges sends one
message.

**D-18.5 — Delivery is best-effort, bounded and off the request path.** One `asyncio.Queue`
(capacity 100) and one dispatcher task in the backend process, cancelled with the other loops in
`lifespan`. A full queue drops the new message, counts it and logs a WARNING. Ingest and the
evaluator never block on a network call. Each send has a 5 s timeout and at most 3 attempts. A
connection error, `429` or `5xx` is retried after 1 s, then 4 s, honouring `Retry-After` up to 30
s. A `3xx` or any other `4xx` is a configuration error: logged as an ERROR and not
retried. Nothing is
persisted. A message still queued at shutdown is lost. That is acceptable because the finding
itself is still in the API.

The HTTP client is the standard library (`urllib.request` in `asyncio.to_thread`), not a new
dependency. The backend has no other outbound HTTP, and three POSTs a minute at most do not
justify a client library. Certificates are verified against the image's CA bundle.

**D-18.6 — Plain words, built from fields, nothing interpreted.** Each message gives a title
that a non-specialist can read, the relationship in `namespace/name (Kind)` form, the port, the
counts, the anomaly's own `reason`, the cluster id and the time in UTC, plus `alerts.uiUrl` as
the last line when it is set:

| Type | Title |
|---|---|
| `CANARY_CONTACT` | Decoy touched |
| `NEW_EXTERNAL_EGRESS` | New connection out of the cluster |
| `FAILURE_SPIKE` | Connections failing |
| `NEW_EDGE` | New connection |
| `SLOW_SETUP` | Connections slower to set up |

Workload names are data from the cluster (ADR-015 §3). Every channel gets them in a form that
cannot become markup. Telegram messages are sent with no `parse_mode`. Slack text has `&`, `<`
and `>` escaped, so a name cannot form a link or an `@channel` mention. Teams puts each line in
its own `TextBlock` and escapes the inline Markdown Adaptive Cards render (`\`, `` ` ``, `*`, `_`,
`~`, `[`, `]`). Messages never contain an IP address:
everything outside the cluster is already the single `EXTERNAL` node (ADR-001 §6, ADR-003).

**D-18.7 — Observable like everything else.** `topology_backend_alerts_total{channel, outcome}`
counts outcomes `sent`, `failed` and `dropped`, with every configured channel's series present
from start-up. Start-up logs one INFO line naming the active channels (names only) and the
severity floor.

**D-18.8 — A test message, with no API route.** `python -m app.alerts test` sends one "alerts are
working" message to every configured channel and exits non-zero if any fails. The operator runs
it with `kubectl exec`, wrapped as `make alerts-test`. For the kind demo, `make alerts-on` reads
the credentials from the environment, writes them to a Secret with `kubectl` (not `helm --set`,
which would keep them in the release history) and points the release at it. It is not an HTTP route, because the API
listener is read-only (ADR-014 D-14.3) and should stay that way.

## 3. Consequences

**Topology leaves the cluster, by the operator's choice.** A third party (Slack, Microsoft or
Telegram) receives the names of the workloads involved in each alert, their namespaces, ports and
connection counts. That is far less than the whole graph, but it is not nothing, and a Telegram
bot chat is not end-to-end encrypted. The values file says so next to the switch, and
`limitations.md` records it.

**One new kind of egress.** The backend NetworkPolicy is ingress-only (ADR-014), so the kind
demo needs no change. A cluster with a default-deny egress policy must allow the backend to reach
the configured hosts on 443, and DNS. The operator guide gives that rule. The chart does not
write it, because the right egress rule depends on the cluster's DNS and proxy setup, which the
chart cannot know.

**At-most-once, best-effort.** A finding can be missed: queue full, channel down for longer than
the retries, backend restarting. The API, log and gauge remain the record. Alerts tell you to go
and look. They do not replace that record.

**Canary alerts are fast; the rest wait for the evaluator.** A canary message goes out at most a
few seconds after the agent's next flush. Every other type waits up to
`ANOMALY_EVALUATION_SECONDS` (60 s).

## 4. Alternatives rejected

**Route through Alertmanager only.** It is the right path when it exists, and it is still
available (ADR-016's gauges). Requiring it would make Prometheus mandatory for this feature,
against ADR-001 §4.2, and an alert rule on a gauge adds a scrape interval and a `for:` clause to
the canary's latency.

**A generic "any URL" webhook.** It is simple to build, but it accepts an arbitrary destination
and leaves the operator to write the receiver. Three named channels can each be tested against
their documented format.

**Sending from the frontend or a separate notifier Deployment.** The backend is the only component
that sees both ingest and the evaluator. A second Deployment would need its own credentials to
the API and would add a pod to the demo for three POSTs a minute.

**Persisting an outbox in PostgreSQL for at-least-once delivery.** That means a schema change, a
retry worker and deduplication on the receiving side for a notification that already has a
durable record in the API. It is out of proportion to the job.

**An `httpx` dependency.** It is a better client, but the stdlib does three POSTs correctly, and
every runtime dependency is one more thing the image scan gate (ADR-014 D-14.11) has to clear.

## 5. Tests

| ID | Assertion | Where |
|---|---|---|
| T-18.1 | Settings: alerts off by default; enabled with no channel refuses to start; non-https and non-Slack-host URLs rejected | `backend/tests/test_alerts.py` |
| T-18.2 | Each channel's request body matches its documented shape; hostile names stay inert in all three | `backend/tests/test_alerts.py` |
| T-18.3 | Canary contact at ingest enqueues once per `(source, canary, port)` per cooldown; a replayed batch enqueues nothing | `backend/tests/test_alerts.py` |
| T-18.4 | Evaluator: first pass silent; later passes send only newly active ids at or above the floor; `CANARY_CONTACT` never sent from this path; one message per pass, "and N more" past 5 | `backend/tests/test_alerts.py` |
| T-18.5 | Delivery against a local HTTP server: retries 429/5xx and honours `Retry-After`; no retry on 400; no redirect followed; full queue drops and counts | `backend/tests/test_alerts.py` |
| T-18.6 | No URL or token appears in any log record, including on failure | `backend/tests/test_alerts.py` |
| T-18.7 | `/metrics` exposes `topology_backend_alerts_total` for each configured channel | `backend/tests/test_alerts.py` |
| T-18.8 | Chart: off renders no Secret and no `ALERT_*` env; on with `--set` renders the Secret; `existingSecret` renders none; schema validates | `scripts/verify-chart.sh` |
| T-18.9 | Kind, with a real Telegram bot: `make alerts-test` arrives; `make demo-canary` arrives within seconds; a rolling restart sends nothing | manual, recorded below |

## 6. Tracker

- [x] **N18-1** ADR-018, index and plan entry
- [x] **N18-2** Backend: settings, channel formatters, dispatcher, metrics — D-18.1, D-18.2, D-18.5 – D-18.7, T-18.1, T-18.2, T-18.5 – T-18.7
- [x] **N18-3** Backend: ingest and evaluator triggers, cooldown, first-pass seed, severity floor, batching — D-18.3, D-18.4, T-18.3, T-18.4
- [x] **N18-4** `python -m app.alerts test`, `make alerts-test` — D-18.8
- [x] **N18-5** Chart: values, schema, Secret, env, comments — D-18.2, T-18.8
- [x] **N18-6** Docs: operator guide (setup per channel, egress rule), limitations, README, demo script
- [ ] **N18-7** Verification on kind — T-18.9 · run 2026-10-29, see below; open: a message
  actually arriving on a phone (needs the owner's Slack/Teams webhook or a network that reaches
  Telegram)

### Verification on kind, 2026-10-29

Backend image rebuilt from this branch; `make demo-up`, then `make alerts-on`.

| Check | Result |
|---|---|
| `chat alerts on` logged at start-up with channel names only | met |
| `make alerts-test`, Teams URL on a non-existent host | `no response` after 3 attempts, exit 1 — as designed |
| HTTPS with certificate verification from the backend image | met: `hooks.slack.com` and `www.google.com` answered over verified TLS in < 1 s |
| Telegram from this network | **not reachable**: DNS resolves, TCP to `api.telegram.org`'s IPv4 hangs to the timeout (also from the kind node). A network property, not the code; the demo must use Slack or Teams, or a network that reaches Telegram |
| `make demo-canary` with a Slack webhook (fake path) | canary contacted 22:53:56, Slack answered 22:53:57 with `404`: logged as `alert rejected by the channel`, 1 attempt, not retried — the full ingest → queue → HTTPS path |
| Cooldown | three canary contacts (10 attempts) in 70 s, one send |
| `topology_backend_alerts_total{channel="slack"}` | `failed 1`, `sent 0`, `dropped 0`, all three series present |
| Credential in the log | 0 occurrences of the webhook path |
| Rolling restart | first evaluator pass logged the standing `CANARY_CONTACT` and `NEW_EDGE`, sent nothing; nor did the next pass |
