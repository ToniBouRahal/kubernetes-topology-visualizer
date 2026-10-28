# Operator guide

Installing, configuring and running the system somewhere other than the kind demo.

For what it cannot do, read [`limitations.md`](limitations.md) first — several entries there change
whether this is the right tool for a given cluster.

---

## Requirements

| | |
|---|---|
| Kernel | Linux 5.8+ with BTF at `/sys/kernel/btf/vmlinux` (`CONFIG_DEBUG_INFO_BTF`). No fallback exists. |
| Kubernetes | 1.28+. The agent uses EndpointSlices, not the deprecated Endpoints API. |
| Privileges | The agent runs privileged. Nothing else does. |
| Storage | A PVC for the in-cluster database, or an external PostgreSQL. |

`make preflight` checks the kernel, BTF and host inotify limits before creating a cluster.

## Installing

```bash
helm install topology charts/topology-visualizer \
  --namespace topology --create-namespace \
  --set clusterId=prod-eu-west-1 \
  --set postgresql.enabled=true \
  --set postgresql.auth.password="$(openssl rand -base64 24)" \
  --set auth.externalUrl=https://topology.example.com \
  --set auth.oidc.issuerUrl=https://login.example.com \
  --set auth.oidc.clientId=topology \
  --set auth.oidc.clientSecret.existingSecret=topology-oidc
```

Sign-in is on by default, so the chart will not render without an `externalUrl` and an identity
provider — see [Sign-in with your identity provider](#sign-in-with-your-identity-provider), or
`auth.dex.enabled=true` for a self-contained trial.

`clusterId` is **not** cosmetic: it is part of every node identity, so changing it later makes all
existing history unreachable. It may not contain a colon, and the schema rejects one that does.

### Pointing at an existing database

```bash
kubectl create secret generic topology-db --from-literal=database-url='postgresql://user:pw@host:5432/topology'

helm install topology charts/topology-visualizer \
  --namespace topology --create-namespace \
  --set clusterId=prod-eu-west-1 \
  --set postgresql.enabled=false \
  --set externalDatabaseUrlSecret=topology-db
```

Migrations run at backend startup and the backend refuses to serve until they have. A DSN never
appears in a log line or an API response — it is redacted to `postgresql://user:***@host`.

## Configuration worth knowing about

| value | default | why you might change it |
|---|---|---|
| `clusterId` | `kind-topology` | **Set this.** It is part of every identity. |
| `agent.flushIntervalSeconds` | `10` | Longer means fewer, larger batches and a slower graph. |
| `agent.infrastructurePorts` | control-plane ports | Ports excluded as infrastructure noise rather than topology. |
| `agent.debugRawEvents` | `false` | **Leave off.** Logs source and destination addresses, including external ones. |
| `backend.retentionHours` | `1440` (2 months) | Storage grows with distinct edges × buckets, not with traffic volume — see Capacity for the per-edge cost and the volume size it implies. |
| `networkPolicy.enabled` | `true` | Verified under Calico. Inert on a CNI that ignores NetworkPolicy. |
| `backend.replicaCount` | `1` | The schema rejects more; horizontal scaling is untested, not forbidden. |

## Verifying an install

```bash
make agent-verify      # an agent pod Running on every node, including control-plane
make verify-db-image   # the database image PULLS rather than relying on a side-loaded copy
make demo-verify       # expected edges present, replicas collapsed, counts exact
```

An agent missing from one node means that node's traffic is invisible, and the graph will look
plausible while being incomplete — which is why coverage is asserted rather than assumed.

## Day-to-day operation

### What to watch

```bash
kubectl -n topology port-forward ds/topology-visualizer-agent 9090:9090
curl -s localhost:9090/metrics
```

| metric | what a bad value means |
|---|---|
| `topology_agent_kernel_samples_lost_total` | **> 0 is data loss.** The ring buffer overflowed; connections silently never appeared. |
| `topology_agent_endpoints_unresolved_total` | High relative to observations: the informer cannot map addresses — often churn, sometimes RBAC. |
| `topology_agent_batches_dropped_total` | The delivery queue overflowed; the backend was unreachable for longer than the queue could absorb. |
| `topology_agent_events_filtered_foreign_node_total` | Should be **0** on a real cluster. Non-zero means nodes share a kernel (kind), and counts would otherwise be inflated. |
| `topology_agent_delivery_queue_depth` | Persistently rising: delivery is slower than aggregation. |

The backend exposes `/health/live` and `/health/ready`. Readiness reflects migrations *and* storage,
so it drops to 503 during a database outage and recovers without a restart.

### Anomalies and canaries (ADR-016)

The backend checks the last 5 minutes against the 24 hours before them once a minute, with five
fixed rules, and reports what it finds three ways — none of which leaves the cluster — plus a
fourth, off by default, that does (chat alerts, below):

- the **Anomalies** list in the UI and `kubectl topology anomalies` (both read
  `GET /api/v1/anomalies`, for whatever window is selected);
- a `WARNING` log line, `anomaly detected`, when a finding first appears (not every minute it lasts);
- the gauge `topology_backend_active_anomalies{type="…"}` on the backend's `/metrics`, one series
  per rule, zero included.

| `type` | severity | means |
|---|---|---|
| `canary_contact` | critical | something connected to a workload named in `backend.canaries` |
| `new_external_egress` | critical | a workload reached outside the cluster on a port it did not use in the baseline |
| `new_edge` | warning | a relationship absent from the baseline |
| `failure_spike` | critical | ≥ 20 % of ≥ 10 attempts failed, ≥ 20 points above the baseline |
| `slow_setup` | warning | mean TCP setup ≥ 3× the baseline and ≥ 1 ms slower |

To be told rather than look, alert on the gauge with your own Prometheus, for example
`max(topology_backend_active_anomalies{type=~"canary_contact|new_external_egress"}) > 0`, or on the
log line in your log pipeline. Or switch on the chat alerts below.

**Canaries** are decoys: a plausible-looking workload nothing legitimate calls. Name them in
`backend.canaries` as `namespace/name` (any kind) or `namespace/Kind/name`. Every connection
attempt to one adds to `topology_backend_canary_connections_total` the moment it is ingested, logs
`canary contacted` with the caller, and is marked on the graph. `make demo-canary` deploys one on
the kind demo and touches it.

`backend.anomalies.baselineHours` (default 24) and `backend.anomalies.minAttempts` (default 10)
are the two values worth tuning. A longer baseline costs a longer query, once per request and once
per minute. For the first day after an install the baseline is short and relationships that simply
have not recurred yet show as new — see `limitations.md` §4.6 – §4.8.

### Chat alerts: Slack, Microsoft Teams, Telegram (ADR-018)

Off by default. When on, the backend posts a message when a canary is touched (sent from ingest,
within seconds) or a critical anomaly first appears (sent by the once-a-minute evaluation). One
message per evaluation however many findings it has: five listed, the rest counted.

**This sends part of the topology out of the cluster**: the names, namespaces, kinds and ports of
the workloads in each alert, and connection counts — never an IP address — to the provider you
configure. Decide whether that is acceptable before switching it on.

| Channel | What to create | Chart value |
|---|---|---|
| Slack | An app with an *incoming webhook* for the channel | `alerts.slack.webhookUrl` |
| Microsoft Teams | In the channel: *Workflows* → "Post to a channel when a webhook request is received"; copy the URL it shows | `alerts.teams.webhookUrl` |
| Telegram | A bot from `@BotFather` (its token), added to a group or messaged once; the chat id from `https://api.telegram.org/bot<token>/getUpdates` | `alerts.telegram.botToken`, `alerts.telegram.chatId` |

A webhook URL is a credential — whoever has it can post to the channel — so none of these belongs
in a values file. Either pass them with `--set`, or create a Secret with any of the keys
`slack-webhook-url`, `teams-webhook-url`, `telegram-bot-token` and name it in
`alerts.existingSecret` (the better choice: `--set` values are kept in Helm's release history).

```bash
kubectl -n topology create secret generic topology-chat-alerts \
  --from-literal=telegram-bot-token="$TELEGRAM_BOT_TOKEN"
helm upgrade topology charts/topology-visualizer -n topology --reuse-values \
  --set alerts.enabled=true --set alerts.existingSecret=topology-chat-alerts \
  --set-string alerts.telegram.chatId=-1001234567890 \
  --set alerts.uiUrl=https://topology.example.com
kubectl -n topology rollout restart deploy/topology-visualizer-backend   # a Secret change needs this
make alerts-test      # or: kubectl exec deploy/…-backend -- python -m app.alerts test
```

On kind, `TELEGRAM_BOT_TOKEN=… TELEGRAM_CHAT_ID=… make alerts-on` does all of that. Then
`make alerts-test` sends one "alerts are working" message to every channel and exits non-zero if
any fails.

`alerts.minSeverity` is `critical` by default — decoy touched, new connection out of the cluster,
connections failing. `warning` adds new in-cluster connections and slower setup, which on a busy
cluster is a message every time something deploys. `alerts.canaryCooldownSeconds` (300) is how
long the same caller touching the same decoy on the same port stays quiet after its first message.

Alerts switched on with no channel configured stop the backend at start-up with a message naming
the setting; so does a Slack URL not on `hooks.slack.com`, a non-https URL, or a Telegram token
without a chat id. Each send is logged as `alert sent`, `alert not delivered` (gave up after three
tries on a timeout, 429 or 5xx) or `alert rejected by the channel` (any other status: a revoked
webhook, a wrong chat id) — with the channel name and status, never the URL or token — and counted
in `topology_backend_alerts_total{channel, outcome}`.

**Egress.** The chart's NetworkPolicies restrict ingress only, so nothing more is needed unless the
cluster enforces a default-deny egress policy. Then the backend needs DNS and HTTPS out, to
`hooks.slack.com`, your Teams Workflows host, or `api.telegram.org`. NetworkPolicy cannot name
hosts, so the usual rule is port 443 to anywhere, or your egress proxy (the backend honours
`HTTPS_PROXY`):

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: topology-backend-alerts-egress, namespace: topology}
spec:
  podSelector:
    matchLabels: {app.kubernetes.io/component: backend}
  policyTypes: [Egress]
  egress:
    - ports: [{port: 53, protocol: UDP}, {port: 53, protocol: TCP}]
    - ports: [{port: 443, protocol: TCP}]
```

Delivery is best-effort: a message that cannot be sent is counted and logged, not stored, and a
finding that first appears while the backend is restarting is not sent at all. The API, the log
and the gauge remain the record — see `limitations.md` §4.9.

### Prometheus and Grafana (optional)

The port-forward above is the zero-dependency way to read the metrics, and it stays the default.
If the cluster already runs the Prometheus Operator and a Grafana with the dashboard sidecar — the
kube-prometheus-stack shape — the chart can hand them the endpoints instead
([ADR-011](adr/ADR-011-optional-prometheus-grafana.md)):

```bash
helm upgrade --install topology charts/topology-visualizer -n topology \
  --set monitoring.enabled=true \
  --set monitoring.monitorLabels.release=kube-prometheus-stack   # whatever your Prometheus selects on
```

That renders a `PodMonitor` for the agent (one target per node), a `ServiceMonitor` for the
backend, and a ConfigMap carrying the **Topology Visualizer — pipeline health** dashboard, which
the sidecar imports within its poll interval. With `networkPolicy.enabled`, the backend policy also
admits scrapes from `monitoring.prometheusNamespace` (default `monitoring`); without that rule an
enforcing CNI would show the backend as `up == 0`.

The dashboard is the table above over time and per node — whether the graph can be trusted right
now — not the topology. The graph stays the only place the topology is drawn. Nothing installs
Prometheus or Grafana: if the CRDs are absent, the install fails with `no matches for kind
"PodMonitor"`, which is the intended message rather than a scrape that silently never happens.

### Grafana links from the details panel (optional)

The other direction: selecting a workload in the UI offers **Metrics** and **Logs** buttons that
open the cluster's own Grafana for that workload, over the window the panel is showing
([ADR-012](adr/ADR-012-grafana-deep-links.md)). Navigation only — nothing is embedded or queried,
the tab lands on Grafana's own login if you are not signed in, and the panel says the numbers
there are the cluster's, not this tool's.

```bash
helm upgrade --install topology charts/topology-visualizer -n topology \
  --set frontend.grafana.url=https://grafana.example.com \
  --set frontend.grafana.lokiDatasourceUid=<uid>          # omit for no Logs button
```

*Metrics* opens `frontend.grafana.workloadDashboardUid` with `var-namespace`, `var-type` and
`var-workload`. The default is `topology-workload`, a dashboard this chart ships through the same
sidecar ConfigMap as the pipeline one (CPU, memory, network and restarts per pod, from cAdvisor and
kube-state-metrics). On kube-prometheus-stack, `a164a7f0339f99e89cea5cb47e9be617` — its *Kubernetes
/ Compute Resources / Workload* — is a richer alternative that takes the same three variables.
*Logs* opens Explore on the Loki datasource with `{namespace="…", pod=~"<workload>-.*"}` — a
prefix match, so a workload named `backend` also matches `backend-worker` pods
(`limitations.md`). Standalone pods get an exact-match logs link and no metrics link; `Service`
and `EXTERNAL` nodes get neither, because there is no single workload behind them.

The values reach the browser as `/config.json` from a ConfigMap; a change is a `helm upgrade`,
which rolls the frontend, not an image rebuild.

### No Prometheus or Grafana yet? Bundle them (optional)

For a cluster that has neither — the kind demo, or a first try on a real one — one flag installs a
plain Prometheus (with kube-state-metrics) and Grafana beside the release, every image pinned by
digest ([ADR-013](adr/ADR-013-bundled-observability.md)):

```bash
make demo-observability          # on the kind demo: upgrade + print the password and port-forward
# or, by hand, on any cluster:
helm upgrade --install topology charts/topology-visualizer -n topology --reuse-values \
  --set observability.enabled=true \
  --set frontend.grafana.url=http://localhost:3000     # however YOUR browser reaches Grafana
```

Then:

```bash
kubectl -n topology get secret topology-grafana -o jsonpath='{.data.admin-password}' | base64 -d; echo
kubectl -n topology port-forward svc/topology-grafana 3000:80
```

Log in as `admin`; both dashboards appear within a minute, and the UI's *Metrics* buttons open the
workload one. What this bundle deliberately is not: alertmanager, node-exporter, Loki (so no *Logs*
button), persistence (dashboards are re-provisioned from the ConfigMap on every start), or a
production monitoring stack — its Prometheus keeps two days. It scrapes by annotation, so it is the
alternative to `monitoring.*`, not a companion: the chart refuses both flags at once.

The one cost that is not optional: the chart now declares these two dependencies, and Helm will not
render or install it until they are fetched — `make chart-deps` (run automatically by `demo-up`,
`lint-helm` and CI) does it once, ~200 KB from the two chart repositories.

### Capacity

Storage is proportional to **distinct edges × buckets retained**, not to traffic volume: a busy edge
and a quiet one occupy the same row. One minute-bucket per edge per minute, `retentionHours` deep.

Measured on the running demo rather than estimated — 190-byte average tuple, and about 700 bytes a
row once the primary key and three indexes are counted:

```text
≈ 1 MB per distinct edge per day      (1,440 minute-buckets × ~700 B)
≈ 60 MB per distinct edge for the 1440-hour default
```

So the shipped 2Gi database volume holds roughly **30 distinct edges for two months**, or 900 edges
for a day. The demo topology has 8. Past that, raise `postgresql.persistence.size` — and raise it
*before* installing: a StatefulSet's `volumeClaimTemplates` are immutable, so changing the value on
a running release does nothing and the volume can only grow by recreating it.

Only edges that actually carried traffic in a minute occupy a bucket for it, so an idle cluster
costs nothing and the figures above are the busy-case ceiling.

Measured on the demo cluster: agent ~35 MiB RSS per node, 1,325 events/s sustained with no kernel
drops, graph query p95 62 ms at 500 nodes / 2,000 edges.

**The interface draws up to 2,000 edges**, the backend's default `GRAPH_MAX_EDGES`, with first
paint around 1 s and clicks under 100 ms. When a workload appears or disappears at that size, the
page pauses for about 0.7 s while it re-runs the layout (`limitations.md` §4.1). Raising
`GRAPH_MAX_EDGES` past 2,000 serves more to the API than the canvas will draw; the canvas keeps the
busiest 2,000 edges and says so.

## Security posture

Hardened by [ADR-014](adr/ADR-014-security-hardening.md). In one paragraph: nobody reads the topology
without signing in, nobody writes it without the agent's client certificate, every connection
between components is TLS (mutual, where one side is ours), and the agent holds two capabilities
instead of privilege.

- **Sign-in on every path.** oauth2-proxy in the frontend pod; nginx asks it before every request.
  A page without a session is sent to sign in, an API call gets `401`. With `auth.cli.enabled`, an
  ID token from the same provider is accepted as a bearer credential too
  ([Reaching the API outside the UI](#reaching-the-api-outside-the-ui)).
- **Mutual TLS inside the cluster.** Agent → backend and frontend → backend present client
  certificates from two different CAs, and each backend listener trusts only one of them. Backend →
  PostgreSQL is TLS with `verify-full`; PostgreSQL rejects any connection without TLS.
- **The backend's three ports:** `8443` ingest (agents), `8444` API (the frontend), `8000` health and
  `/metrics` only, in plain HTTP for probes and Prometheus. No topology crosses `8000`.
- **Least privilege.** The agent: `CAP_BPF` and `CAP_PERFMON` only, read-only root, default seccomp,
  one read-only host path. Every other workload meets the `restricted` Pod Security Standard. RBAC is
  `get`/`list`/`watch` for the agent and nothing for anything else.
- **Browser headers:** a Content-Security-Policy with no inline or `eval` script, framing refused,
  `nosniff`, no referrer, and a request-rate limit on `/api/`.
- No packet payload is read. No individual external IP is persisted or returned.

### Sign-in with your identity provider

Register an OIDC client with redirect URI **`<externalUrl>/oauth2/callback`** — exactly that — then:

```bash
kubectl -n topology create secret generic topology-oidc --from-literal=client-secret='…'
helm upgrade --install topology charts/topology-visualizer -n topology \
  --set auth.externalUrl=https://topology.example.com \
  --set auth.oidc.issuerUrl=https://login.example.com \
  --set auth.oidc.clientId=topology \
  --set auth.oidc.clientSecret.existingSecret=topology-oidc \
  --set 'auth.oidc.allowedGroups={platform-team}'
```

`allowedEmailDomains` and `allowedGroups` decide who may sign in; everyone admitted can read
everything (the application is read-only). Sessions last `auth.sessionHours` (8). Serve the UI
over HTTPS: the session cookie is `Secure`, which a browser honours on `http://` only for localhost.

For a cluster with no provider of its own, the chart bundles two, never both at once:
`auth.dex.enabled` — a bundled Dex with one user (`admin@topology.local`, password in the
`topology-visualizer-auth` Secret), for trials; see `limitations.md` §6.2 — and
`auth.keycloak.enabled`, a bundled Keycloak that brokers sign-in to Google, Microsoft Entra ID or
another OIDC provider ([below](#the-bundled-keycloak-sign-in-with-google-entra-id-or-another-oidc-provider)).
The kind demo uses Keycloak.

`auth.enabled: false` turns sign-in off. The topology is then readable by anyone who reaches the
frontend Service, and the install notes say so every time.

### The bundled Keycloak: sign-in with Google, Entra ID or another OIDC provider

[ADR-017](adr/ADR-017-keycloak-identity-broker.md). Keycloak is one issuer that the browser and the
CLI both trust, and it *brokers* to upstream providers: people sign in with the Google, Entra ID or
corporate account they already have. Nothing about oauth2-proxy, nginx or the backend changes; they
see a Keycloak ID token either way.

```bash
make image-keycloak     # builds the optimized image and side-loads it into kind
helm upgrade --install topology charts/topology-visualizer -n topology \
  --set auth.externalUrl=https://topology.example.com \
  --set auth.keycloak.enabled=true
```

`auth.keycloak.enabled` and `auth.dex.enabled` are mutually exclusive: the chart fails rendering
when both are true. With neither, `auth.oidc.*` is required as before. `make image-keycloak` builds
`keycloak/Dockerfile` — the upstream image pinned by digest, pre-built (`kc.sh build`) so that it
runs with a read-only root filesystem — and `make demo-up` does it for you.

```yaml
auth:
  keycloak:
    enabled: false
    image: {repository: topology-keycloak, tag: dev, pullPolicy: IfNotPresent}
    demoUser: {enabled: true, email: admin@topology.local}   # password generated into <fullname>-auth, key demo-password
    identityProviders:
      google:    {enabled: false, clientId: "", clientSecret: {existingSecret: "", key: client-secret}, hostedDomain: ""}
      microsoft: {enabled: false, tenantId: "", clientId: "", clientSecret: {existingSecret: "", key: client-secret}}
      oidc: []   # each: {alias, displayName, issuer, authorizationUrl, tokenUrl, jwksUrl, clientId, clientSecret: {existingSecret, key}, scopes}
```

**The demo user.** With `demoUser.enabled` (the default), the realm has one local user,
`admin@topology.local`, whose password is generated into the `<fullname>-auth` Secret under
`demo-password` — the same Secret and key as Dex's:

```bash
kubectl -n topology get secret topology-visualizer-auth -o jsonpath='{.data.demo-password}' | base64 -d
```

Repeated wrong passwords lock it out for a while: five failures, then a wait that grows by 60 s per
failure up to 15 minutes, never permanently. When people sign in only through upstream providers,
turn the demo user off.

**What it is and is not.**

- **The realm is code.** It is rendered from values on every start, and Keycloak's state lives in
  an `emptyDir`. A restart of the Keycloak pod — a rollout, a rescheduled pod —
  signs everyone out, browser and CLI; brokered users are re-created at their next sign-in
  (`limitations.md` §6.15).
- **No admin console.** There is no admin account, and nginx returns `404` for `/keycloak/admin/`,
  the `master` realm and everything else under `/keycloak/` that sign-in does not use (the account
  console, dynamic client registration, the device and CIBA endpoints, SAML, token introspection).
  Change configuration in values and run `helm upgrade`.
- **Admission is oauth2-proxy's, as before.** `auth.oidc.allowedEmailDomains` applies to every
  account, local or brokered. `auth.oidc.allowedGroups` is refused — the chart fails — because the
  realm has no groups (`limitations.md` §6.18).
- **Both clients require PKCE (S256)** and allow the authorisation-code flow only; the CLI's
  device flow is off. ID tokens last one hour.
- **Upstream providers are reached over the Internet** from the Keycloak pod. On an air-gapped
  cluster only the demo user works (`limitations.md` §6.16).

#### Upstream providers

Each provider is enabled only when configured. Its client secret comes from a Secret you create;
the chart passes it to Keycloak as an environment variable, so it never appears in a rendered
manifest. The redirect URI to register with every provider is

```
<externalUrl>/keycloak/realms/topology/broker/<alias>/endpoint
```

where `<alias>` is `google`, `microsoft`, or the `alias` you give an `oidc` entry. Keycloak trusts
the e-mail each provider returns as verified — oauth2-proxy refuses an unverified one — so that
trust is bounded by the pinning described for each (`limitations.md` §6.17). After the upgrade,
the sign-in page shows one button per provider.

| Values key | Pinned to | E-mail taken from |
|---|---|---|
| `identityProviders.google` | optionally one Workspace domain (`hostedDomain`) | `email` |
| `identityProviders.microsoft` | exactly one Entra tenant (`tenantId`) | `preferred_username` (the UPN) |
| `identityProviders.oidc[]` | the given `issuer`, signatures checked against `jwksUrl` | `email` |

**Google (Gmail or Workspace).**

1. In the Google Cloud Console, open *APIs & Services → OAuth consent screen* and configure it
   first: Google will not issue a client without one. An *Internal* audience limits sign-in to
   your Workspace organisation; *External* admits any Google account, subject to the values below.
2. *APIs & Services → Credentials → Create credentials → OAuth client ID*, application type
   **Web application**.
3. Under *Authorised redirect URIs*, add
   `<externalUrl>/keycloak/realms/topology/broker/google/endpoint` — on kind,
   `http://localhost:8080/keycloak/realms/topology/broker/google/endpoint`. Google accepts `http`
   only for `localhost`; anywhere else the URI must be `https`.
4. Copy the client ID and client secret, then:

```bash
kubectl -n topology create secret generic topology-idp-google --from-literal=client-secret='…'
helm upgrade topology charts/topology-visualizer -n topology --reuse-values \
  --set auth.keycloak.identityProviders.google.enabled=true \
  --set auth.keycloak.identityProviders.google.clientId='…apps.googleusercontent.com' \
  --set auth.keycloak.identityProviders.google.clientSecret.existingSecret=topology-idp-google \
  --set auth.keycloak.identityProviders.google.hostedDomain=example.com
```

`hostedDomain` restricts sign-in to accounts of one Workspace domain: Google returns it as the
`hd` claim and Keycloak checks it. Leave it empty to admit any Google account, Gmail included —
then `auth.oidc.allowedEmailDomains` is the only filter, so set it.

**Microsoft Entra ID.**

1. In the Entra admin center (or the Azure portal), *App registrations → New registration*.
2. *Supported account types*: **Accounts in this organizational directory only (single tenant)**.
3. *Redirect URI*: platform **Web**, `<externalUrl>/keycloak/realms/topology/broker/microsoft/endpoint`.
4. *Certificates & secrets → New client secret*; copy its **Value** (shown once), not its ID.
5. From *Overview*, copy the **Directory (tenant) ID** and the **Application (client) ID**.

```bash
kubectl -n topology create secret generic topology-idp-microsoft --from-literal=client-secret='…'
helm upgrade topology charts/topology-visualizer -n topology --reuse-values \
  --set auth.keycloak.identityProviders.microsoft.enabled=true \
  --set auth.keycloak.identityProviders.microsoft.tenantId='<directory-tenant-id>' \
  --set auth.keycloak.identityProviders.microsoft.clientId='<application-client-id>' \
  --set auth.keycloak.identityProviders.microsoft.clientSecret.existingSecret=topology-idp-microsoft
```

Two things differ from what you might expect:

- **One tenant, always.** `tenantId` is required and must be the Directory (tenant) ID, a GUID —
  not a domain, which never matches the issuer Entra's tokens carry, and not `common`,
  `organizations` or `consumers`, which would admit other tenants; the schema refuses all of
  these. Keycloak checks the issuer `https://login.microsoftonline.com/<tenant>/v2.0` on
  every token, so neither another organisation's accounts nor personal Microsoft accounts can sign
  in. This is also why the chart uses Keycloak's generic OIDC provider rather than its built-in
  `microsoft` one, which signs in through the multi-tenant `common` endpoint.
- **The e-mail is the user principal name.** It is taken from `preferred_username` — the UPN, in a
  domain the tenant has verified — not from the `email` claim, which Entra lets users and guests
  set to an address they do not own (the "nOAuth" problem). A user whose UPN differs from their
  mail address signs in as the UPN, so `allowedEmailDomains` must list the UPN's domain.

**Any other OIDC provider** — Okta, Auth0, ADFS, another Keycloak, a corporate SSO. Register a
confidential web client with the redirect URI above (`<alias>` being the alias you choose), create
the Secret, and add an entry to `oidc`:

```yaml
auth:
  keycloak:
    identityProviders:
      oidc:
        - alias: corp                 # also the last-but-one segment of the redirect URI
          displayName: Corporate SSO  # the button label
          issuer: https://sso.example.com
          authorizationUrl: https://sso.example.com/oauth2/authorize
          tokenUrl: https://sso.example.com/oauth2/token
          jwksUrl: https://sso.example.com/oauth2/keys
          clientId: topology
          clientSecret: {existingSecret: topology-idp-corp, key: client-secret}
          scopes: openid email profile
```

The endpoints are the ones in the provider's `/.well-known/openid-configuration`. Tokens must come
from `issuer` and verify against `jwksUrl`; the e-mail is the `email` claim, trusted as verified.
Add only a provider that verifies the e-mail addresses it issues, and restrict
`allowedEmailDomains` accordingly.

**The CLI.** With `auth.cli.enabled`, the realm also registers the public `topology-cli` client.
`kubectl topology login` with no `--issuer` probes `<server>/keycloak/realms/topology`, then
`<server>/dex`, and uses the first whose discovery document is valid; the choice is saved with the
credential. The user signs in on the same Keycloak page, with any of its buttons.

### Certificates

The chart generates three CAs and five certificates on first install and keeps them across
upgrades. The CA private keys are never stored. To rotate — before the 365-day expiry, or after a
suspected leak:

```bash
kubectl -n topology delete secret topology-visualizer-tls-{server,postgresql,agent,frontend,keycloak,client-cas} topology-dex-tls
helm upgrade topology charts/topology-visualizer -n topology --reuse-values
```

A new set is generated and every pod rolls onto it. To have cert-manager issue and renew them
instead, set `tls.generate: false` and create Secrets with the same names and keys (`tls.crt`,
`tls.key`, `ca.crt`; `ingest-ca.crt` and `api-ca.crt` for `-client-cas`).

### Reaching the API outside the UI

People read the topology from a terminal with `kubectl topology`, a kubectl plugin in `cli/`
([ADR-015](adr/ADR-015-cli-and-mcp-clients.md)). It signs in through the same identity provider
as the browser and sends the resulting ID token as `Authorization: Bearer`. The request takes the
browser's path: nginx, then oauth2-proxy, which verifies the token and applies the same
`allowedEmailDomains` and `allowedGroups`, then mTLS to the backend. nginx removes the
`Authorization` header before the backend, so the backend never sees a token. The request log
records the user's e-mail as it does for the browser (`limitations.md` §6.7). The CLI runs on the
user's machine. It is not deployed in the cluster and never talks to the Kubernetes API.

It is off until the chart allows it:

```bash
helm upgrade topology charts/topology-visualizer -n topology --reuse-values \
  --set auth.cli.enabled=true
```

| value | default | |
|---|---|---|
| `auth.cli.enabled` | `false` (`true` in the kind values) | oauth2-proxy accepts bearer ID tokens (`--skip-jwt-bearer-tokens`, `--oidc-extra-audience`). Ignored when `auth.enabled` is false. |
| `auth.cli.clientId` | `""` | The CLI's public client. With the bundled Dex, empty means `topology-cli`, which the chart registers. **Required** with an external provider; may not be `topology` with the bundled Dex. |
| `auth.cli.refreshHours` | `8` | Bundled Dex or Keycloak only: the refresh-token lifetime, i.e. how long a CLI stays signed in without opening the browser again (1 – 168). |

Then, on the user's machine, with the same port-forward or URL the browser uses:

```bash
make cli                                   # builds bin/kubectl-topology; put it on PATH
kubectl topology login --server https://topology.example.com
kubectl topology deps backend -n shop --since 1h
kubectl topology status
kubectl topology logout --server https://topology.example.com
```

`--server` must be `auth.externalUrl` exactly: the issuer the tokens carry is built from it, so on
kind it is `http://localhost:8080` (the default), not `127.0.0.1`. `login` opens the browser,
listens on the first free of `127.0.0.1:38085`, `38086` and `38087` for the callback, and gives up
after five minutes; it prints the URL when it cannot open a browser. The credentials are stored in
`~/.config/kubectl-topology/credentials.json` (`~/Library/Application Support/…` on macOS), mode
`0600`. The CLI refuses a file or directory that other users can read and prints the `chmod` that
fixes it. With sign-in off (`auth.enabled: false`), the commands need no login, and `login` says
the server does not require sign-in.

Exit codes, for scripts: `0` success, including an empty result; `1` usage error or an ambiguous
name; `2` not signed in, or the token was refused; `3` another API error; `4` the server or issuer
could not be reached.

The CLI sends a token, code or refresh token over `http://` only to a loopback host, which covers
a port-forward. A plain-HTTP ingress or issuer is refused, so serve both over HTTPS. `--ca-file`
adds a private CA for the server and the issuer. No flag turns TLS verification off.

**An external provider.** Register a second client next to the browser one: public (also called
"native" or "no secret"), authorisation-code flow with PKCE, redirect URIs exactly
`http://127.0.0.1:38085/callback`, `http://127.0.0.1:38086/callback` and
`http://127.0.0.1:38087/callback` (or the provider's rule for any loopback port). Set its ID as
`auth.cli.clientId`, and pass the issuer and client ID once at login; they are saved with the
credential:

```bash
helm upgrade topology charts/topology-visualizer -n topology --reuse-values \
  --set auth.cli.enabled=true --set auth.cli.clientId=topology-cli
kubectl topology login --server https://topology.example.com \
  --issuer https://login.example.com --client-id topology-cli
```

The CLI requests `openid email profile offline_access`, plus `groups` when the provider's discovery
document lists it. If admission depends on a claim that comes from another scope (whatever
`auth.oidc.scopes` requests for the browser), pass `--scopes` with that scope included. A token
without an e-mail, or without the groups `allowedGroups` needs, is refused with `401` like a
browser sign-in would be. oauth2-proxy accepts ID tokens for exactly two audiences: the browser
client and `auth.cli.clientId`. A token the same provider issued to any other client is refused.

**Lifetimes.** With the bundled Dex and `auth.cli.enabled`, ID tokens last one hour (Dex's default
is 24 hours) and the CLI refreshes them on its own. Refresh tokens last `auth.cli.refreshHours`,
and expire sooner after eight hours unused. The one-hour lifetime applies to browser sign-ins too,
but it does not shorten a browser session, which lasts `auth.sessionHours` as before. With an
external provider the lifetimes are the provider's: keep ID tokens short, because they are what a
stolen token is good for. The bundled Keycloak sets the same bounds: one-hour ID tokens, and a
session — which every refresh token belongs to — that ends after eight hours unused or
`auth.cli.refreshHours` in all. The CLI's `offline_access` request is granted — Keycloak refuses a
request naming a scope the client lacks, so the CLI's client has that one scope — but the realm
bounds offline sessions by the same eight hours unused and `auth.cli.refreshHours` in all, instead
of Keycloak's default 30 days.

**No revocation, and cutting a user off.** `kubectl topology logout` deletes the local credential
and nothing else (`--all` deletes every server's credential). Neither Dex nor oauth2-proxy can
revoke an ID token already issued, so a copied token stays valid until it expires: at most one hour
with the bundled Dex (`limitations.md` §6.9). To cut a user off sooner, remove the user at the
provider. With the bundled Dex, restart it: refresh tokens live only in its memory, so every CLI
must sign in again at its next refresh. An ID token issued before the restart is still accepted
until its `exp` (measured, §6.10), so a restart cuts a user off within the hour, not at once.

```bash
kubectl -n topology rollout restart deployment/topology-dex
```

The bundled Keycloak is cut off the same way, with the same caveat for issued ID tokens: its
sessions live only in the pod, so a restart ends every one
(`kubectl -n topology rollout restart deployment/topology-visualizer-keycloak`). A `helm upgrade`
that changes the realm restarts it too, with the same effect: the pod rolls when the rendered realm
changes.

**The MCP server.** `kubectl topology mcp` serves five read-only tools (`list_namespaces`,
`get_topology`, `get_dependencies`, `diff_topology`, `get_edge`) over stdio to an AI agent on the
user's machine. It reads the stored credential on every call and never opens a browser itself. When
it reports that the user is not signed in, the user runs `kubectl topology login` once. Before you
enable `auth.cli`, know two things and tell your users:

- **The agent can read everything the user can**: which workloads talk to which, namespaces, ports,
  and connection and failure counts. An agent backed by a hosted model may send all of it to the
  model's provider. That map is the sensitive artefact ADR-014 protects.
- **Tool output is cluster data, not instructions.** Workload and namespace names are chosen by
  whoever can create workloads, and reach the agent's context verbatim (`limitations.md` §6.11).
  Every tool description says so; an agent with write access to the user's code and shell should
  not act on text that appears in a name.

**Operators with Secret access** can still read the API directly as the frontend, which needs no
sign-in and no `auth.cli`:

```bash
make api-get API_PATH='/api/v1/graph?window=5m'   # the kind demo
```

`scripts/lib/mtls.sh` is what that uses: it fetches the frontend's client certificate from the
Secret into a private temporary directory, port-forwards the API port, and verifies the backend by
its Service name. That is one shared identity with no user in the log, which is why it is not the
route for people.

### Checking it

| | |
|---|---|
| `make verify-tls` | from an unlabelled pod: the mTLS ports refuse it, `8000` serves no topology, PostgreSQL refuses plain TCP |
| `make pod-security` | labels the namespace: enforce `privileged` (the agent), warn and audit `restricted` |
| `make audit` | govulncheck, pip-audit, npm audit |
| `make scan-images` | Trivy on every image, CycloneDX SBOMs in `sbom/` |

## Upgrading

```bash
helm upgrade topology charts/topology-visualizer -n topology --reuse-values
```

History survives: it lives in PostgreSQL, keyed by identities that do not change across restarts.
Verified by pinning the *oldest* bucket and confirming it is unchanged — new traffic lands in recent
buckets, so a total alone cannot distinguish growth from double-counting.

Do not change `clusterId` on an upgrade. Existing rows carry the old value and become unreachable.

## Uninstalling

```bash
helm uninstall topology -n topology
kubectl delete pvc -n topology -l app.kubernetes.io/name=topology-visualizer   # history, deliberately kept by default
```

`helm uninstall` leaves the PVC, so history survives a reinstall. Delete it explicitly when you mean
to lose the data.


## Upgrading for connection outcomes

Deploy the updated backend first. Migration `002_connection_outcomes.sql` adds nullable failure
counts and zero-sample timing columns; historical rows remain unmeasured. Existing agent batches
are accepted. Then roll out the updated agents and frontend. New agents emit additive fields in
schema version 1 that older backends reject, so do not upgrade agents first or roll the backend
back while those agents are active.

Monitor `topology_agent_setup_tracking_missed_total` (a terminal outcome without a recorded start,
including eviction or attachment boundaries) and `topology_agent_setup_tracking_failed_total`
(failed tracking map updates), alongside `topology_agent_kernel_samples_lost_total`. Missing starts
remove timing samples but do not suppress terminal outcome counts. Setup tracking uses a bounded
65,536-entry LRU map per agent.

The UI says failed/aborted deliberately: cancellation and failure share the same terminal state.
Inspect application logs or other network diagnostics for a cause. Successful setup timing includes
only measured samples; it is neither an HTTP latency metric nor a failure-rate denominator.
