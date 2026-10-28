# ADR-017: Bundled Keycloak as Identity Broker — Single Sign-On with Google and Microsoft Entra ID

- **Status:** Accepted for implementation
- **Date:** 2026-10-27
- **Parent:** ADR-014 D-14.6 (sign-in through oauth2-proxy, any OIDC provider), D-14.7 (a bundled
  provider for clusters that have none) · ADR-015 D-15.6 – D-15.10 (the CLI's public client,
  bearer tokens, token lifetimes)
- **Amends:** ADR-014 D-14.2 (the `server` CA also signs Keycloak's certificate), D-14.7 (a second
  bundled provider, Keycloak, alongside Dex and mutually exclusive with it; the kind demo moves to
  it), D-14.10 (Keycloak's pages get their own CSP) · ADR-015 D-15.9 (the CLI's default issuer is
  probed: `<server>/keycloak/realms/topology`, then `<server>/dex`) · ADR-007 D-7.5 (`auth.keycloak.*`)
- **Component path:** `keycloak/`, `charts/topology-visualizer/templates/{keycloak,auth,pki,networkpolicies,frontend-deployment}.yaml`,
  `frontend/nginx/`, `frontend/Dockerfile`, `cli/internal/auth/`, `Makefile`, `scripts/`
- **Owning phase:** Post-Phase-5 extension

## 1. Context

ADR-014 made sign-in OIDC and bundled Dex so the kind demo has a provider. Dex is a deliberately
small, static provider: one password user, in-memory state, no lockout. That leaves the owner's
next requirement unmet — **people should sign in with the accounts they already have**: a Google
(Gmail or Workspace) account, a Microsoft Entra ID work account, or a corporate SSO provider.

oauth2-proxy can point at any one of those directly (`auth.oidc.*`, ADR-014 D-14.6), but only at
*one*, and each would then need its own CLI client registration (ADR-015 D-15.7). What is wanted is
one issuer that the UI and the CLI trust, which in turn *brokers* to several upstream providers.
That is Keycloak's identity-brokering feature; Dex's connectors could do part of it, but the owner
chose Keycloak, and it brings three things Dex v2.45.1 cannot do, each of which closes a recorded
limitation:

| Recorded gap | Dex v2.45.1 | Keycloak 26.8 |
|---|---|---|
| limitations §6.14 — PKCE enforced only by the CLI | cannot require it | `pkce.code.challenge.method: S256` per client: no challenge, no code |
| limitations §6.13 — password guessing not rate-limited | no lockout | realm brute-force detection |
| ADR-015 D-15.6 — device flow cannot be switched off | always registered | per-client attribute, plus nginx allowlist |

### What was measured before this ADR (Keycloak 26.8.0, digest `sha256:27b3230f…`)

- The **stock image cannot run with a read-only root filesystem**. Its `start` re-augments Quarkus
  into `/opt/keycloak/lib` whenever a build-time option differs (`--health-enabled` did:
  `transformed-bytecode.jar: Read-only file system`), and `start --optimized` refuses a never-built
  image (*"The '--optimized' flag was used for first ever server start"*).
- An **optimized image** (`kc.sh build` with `KC_DB=dev-file`, `KC_HEALTH_ENABLED=true`,
  `KC_HTTP_RELATIVE_PATH=/keycloak` in the Dockerfile; `start --optimized` at run time) starts with
  a read-only root filesystem and only `/opt/keycloak/data` and `/tmp` writable, in ~12 s.
- With `--hostname=http://localhost:8080/keycloak`, discovery fetched **in-cluster over HTTPS**
  still names the browser-facing issuer and endpoints — the same split ADR-014 D-14.7 relies on
  for Dex.
- `--import-realm` substitutes `${ENV}` placeholders: a client secret written as
  `"${TOPO_SECRET}"` was stored as the environment variable's value. So upstream-provider secrets
  reach Keycloak through `secretKeyRef` env vars and never appear in a rendered manifest.
- Health is served on the management port, 9000, over the same TLS as the main port.

## 2. Decision

**D-17.1 — Keycloak is a second bundled provider, alongside Dex, never both.**
`auth.keycloak.enabled` (default `false`); the chart fails rendering when it and `auth.dex.enabled`
are both true. The kind values switch to Keycloak; Dex stays, unchanged, as the light option and
for ADR-015's verified results. With neither, `auth.oidc.*` is required exactly as before.

**D-17.2 — An optimized image built by this project.** `keycloak/Dockerfile`: the upstream image
pinned by digest, `kc.sh build` with `db=dev-file`, `health-enabled=true`,
`http-relative-path=/keycloak`, `cache=local` (one replica: no JGroups port opened) and the unused
`device-flow`, `ciba`, `impersonation` and `kerberos` features disabled at build time. Built and
side-loaded like the other three images (`make image-keycloak`), scanned by `make scan-images`.

**D-17.3 — State is ephemeral; the realm is code.** `dev-file` on an `emptyDir`. On every start the
chart's realm is imported fresh, so a restart signs everyone out — the same consequence as Dex's
memory storage (limitations §6.10). Brokered users are re-created at their next sign-in. There is
**no admin account**: no `KC_BOOTSTRAP_ADMIN_*`, so the `master` realm has no user, and
configuration changes are made in values, not in a console whose edits a restart would discard.

**D-17.4 — The `topology` realm.** Rendered by the chart into a Secret (it carries generated
secrets), mounted at `/opt/keycloak/data/import`:

| Setting | Value | Why |
|---|---|---|
| `registrationAllowed`, `resetPasswordAllowed`, `rememberMe`, `editUsernameAllowed` | `false` | accounts come from upstream providers or values |
| `bruteForceProtected` | `true`; 5 failures, wait +60 s each, max 15 min, not permanent | closes §6.13 for Keycloak |
| `accessTokenLifespan` | 3600 s | Keycloak's ID-token lifetime; same 1 h as ADR-015 D-15.10 |
| `ssoSessionIdleTimeout` / `ssoSessionMaxLifespan` | 8 h / `auth.cli.refreshHours` (`auth.sessionHours` without the CLI) | bounds every refresh token |
| `offlineSessionIdleTimeout` / `offlineSessionMaxLifespan` | the same 8 h / `refreshHours`, max enforced | bounds the CLI's offline token identically (Keycloak's defaults: 30 days idle, no max) |
| `revokeRefreshToken`, `refreshTokenMaxReuse` | `true`, `0` | rotation, as with Dex |
| client `topology` | confidential, standard flow only, one redirect `<externalUrl>/oauth2/callback`, PKCE S256 required | oauth2-proxy already sends S256 |
| client `topology-cli` (with `auth.cli.enabled`) | public, standard flow only, the three loopback redirects of D-15.7, PKCE S256 required, device grant off | closes §6.14 |
| both clients | default scopes `basic profile email`; `topology-cli` alone has `offline_access` as its one optional scope | no other scope can be requested |

The CLI asks for `offline_access` by default (D-15.8). *Measured on kind:* Keycloak refuses the
whole authorisation request over a scope the client lacks (`invalid_scope`) — it does not ignore
it — so the CLI's client has that scope, and the two offline-session settings above bound the
resulting offline token exactly as the session settings bound an ordinary refresh token. Because
`fullScopeAllowed` is off for both clients, the realm also maps exactly one role, `offline_access`,
into `topology-cli`'s scope; without it the code exchange fails (*"Offline tokens not allowed for
the user or client"*, measured).

A **demo user** (`auth.keycloak.demoUser.enabled`, default `true`) is created with
`email_verified: true` and a password generated into `<fullname>-auth` (`demo-password`), as
Dex's is. It is given the realm's `default-roles-topology` explicitly: a user imported from a file
gets only the roles it lists, unlike one created at sign-in, and without `offline_access` the CLI's
code exchange fails (measured). Operators who sign in only through upstream providers turn it off.

**D-17.5 — Upstream providers (identity brokering).** Each is configured in values and enabled
only when given; each client secret comes from a Secret the operator creates, injected as an env
var and referenced from the realm as `${KC_IDP_<ALIAS>_SECRET}`. All use `syncMode: FORCE` (the
user's name and e-mail are refreshed from upstream at each sign-in) and `trustEmail: true`, because
oauth2-proxy refuses an ID token whose `email_verified` is not `true`; the trust is bounded by the
pinning below. The redirect URI to register with each is
`<externalUrl>/keycloak/realms/topology/broker/<alias>/endpoint`.

| Values key | Keycloak provider | Pinned to | E-mail from |
|---|---|---|---|
| `identityProviders.google` | built-in `google`, alias `google` | optional `hostedDomain` (Workspace domain; Google returns `hd`, Keycloak checks it) | `email` |
| `identityProviders.microsoft` | generic `oidc`, alias `microsoft` | **one tenant**: `tenantId` is required and the schema refuses `common`, `organizations` and `consumers`; the issuer `https://login.microsoftonline.com/<tenant>/v2.0` is checked on every token | `preferred_username` (the UPN, in a domain the tenant has verified) — not the `email` claim, which Entra lets users and guests set ("nOAuth") |
| `identityProviders.oidc[]` | generic `oidc`, operator-chosen alias | the given `issuer`, with signature validation from `jwksUrl` | `email` |

The Microsoft provider is the generic OIDC one, not Keycloak's built-in `microsoft`, because the
built-in one signs in through the multi-tenant `common` endpoint: any Entra tenant, or a personal
Microsoft account, could then sign in. Pinning a tenant is what "sign in with our Entra ID" means.

Admission is unchanged: oauth2-proxy's `allowedEmailDomains` still applies to every account,
local or brokered. `allowedGroups` is **refused with the bundled Keycloak** (the chart fails):
the realm has no groups and its users are ephemeral, so the setting would admit nobody. Group
mapping from Entra is out of scope (§4).

**D-17.6 — Served under `/keycloak` through the frontend; nginx allows only what sign-in uses.**
One port-forward, as with Dex. Keycloak serves **HTTPS only** (`--http-enabled=false`) with a
`server`-CA certificate for `topology-keycloak.<ns>.svc`; nginx and oauth2-proxy verify it.
oauth2-proxy skips discovery and uses split endpoints, exactly as with Dex: the browser-facing
issuer and authorisation URL, the in-cluster token and JWKS URLs. `keycloak-on.conf` is an
**allowlist**:

- proxied: `/keycloak/resources/` (theme assets) and `/keycloak/realms/topology/` — but within the
  realm, `404` for `account`, `clients-registrations` (dynamic client registration),
  `protocol/openid-connect/auth/device`, `protocol/openid-connect/ext/` (CIBA and others),
  `protocol/saml` and `protocol/openid-connect/token/introspect`;
- `404` for everything else under `/keycloak/`: the admin console and REST API, the `master` realm.

Every one of these is `auth_request off`: the sign-in pages must be reachable without a session.

**D-17.7 — Keycloak's pages get their own CSP** (`security-headers-keycloak.conf`), amending
ADR-014 D-14.10 the way ADR-015 D-15.8 did for Dex: `form-action` adds the three CLI loopback
origins, and whatever else the login theme is *measured* to need is added there and recorded with
its reason. Keycloak's own CSP header is hidden so the browser applies exactly one policy.

**D-17.8 — The CLI's default issuer is probed.** With no `--issuer`, `login` tries
`<server>/keycloak/realms/topology` and then `<server>/dex`, taking the first whose discovery
document is valid and names that issuer; a connection failure stops at once. The chosen issuer is
saved with the credential (D-15.9), so later commands do not probe. `--issuer` still overrides.
The probe's discovery does not follow redirects (the shared client does, re-checking each hop
under D-15.14): in Dex mode the Keycloak path answers with a sign-in redirect, which must read as
"not here" rather than be followed to a host that may not resolve and so end the probe as a
connection error. A provider that answers but advertises plain-HTTP endpoints
(`ErrInsecureTransport`) also stops the probe: falling through to Dex would hide a misconfiguration.

**D-17.9 — Kubernetes objects.** A Deployment (one replica), a ClusterIP Service on 8443 (HTTPS)
and 9000 (management, kubelet probes only), a ServiceAccount with no token, a NetworkPolicy
admitting 8443 from the frontend pods only. Pod security: non-root uid 1000, read-only root
filesystem, all capabilities dropped, `RuntimeDefault` seccomp — `restricted`. Resources: request
100m / 512 Mi, limit 1 Gi (the JVM sizes its heap from the limit). A startup probe allows 3 minutes.

**D-17.10 — Signing out ends the Keycloak session too.** oauth2-proxy gets
`--backend-logout-url=<in-cluster realm>/protocol/openid-connect/logout?id_token_hint={id_token}`
and calls it, server-side, on `/oauth2/sign_out`. *Found on kind:* without it, sign-out cleared the
proxy's cookie but not Keycloak's SSO cookie, so the redirect to sign in came straight back with a
code and the user was signed in again without a prompt — the existing E2E test "signing out ends
the session" failed (`200`, expected `401`) and passes with this flag. Dex keeps no browser session,
which is why ADR-014 never needed it.

## 3. Consequences

- The kind demo signs in on Keycloak's page with the same generated demo password; `make demo-up`
  builds one more image. Keycloak takes ~512 Mi and ~15 s to start, against Dex's 32 Mi and 1 s.
- An operator enables Google or Entra by creating a Secret and setting four values; no Keycloak
  console is involved. Without real credentials, the provider buttons render but cannot complete.
- `limitations.md` §6.13 and §6.14 close **for Keycloak**; they stay true for Dex.
- A Keycloak restart signs everyone out, browser and CLI — the same as Dex.
- Upstream providers are reached from the Keycloak pod over the Internet. The chart has no egress
  policy (unchanged), so nothing blocks that; an air-gapped cluster uses the demo user only.

## 4. Alternatives rejected

- **Replace Dex.** The owner kept it: smaller, and ADR-015's verification was done against it.
- **Keycloak's state in the bundled PostgreSQL.** Survives restarts and allows console edits, but
  couples sign-in to `postgresql.enabled`, adds a second database and credentials, and makes the
  console the source of truth instead of values. The owner chose ephemeral state.
- **The Bitnami or codecentric Keycloak charts.** Bitnami's images moved behind a subscription;
  codecentric's `keycloakx` is a thin wrapper that would still need this project's image (D-17.2)
  and most of its values overridden. One Deployment template is smaller.
- **The stock image with a writable root filesystem.** Works, but gives up ADR-001 §6's read-only
  root for the most exposed component. Measured unnecessary (§1).
- **Keycloak's built-in `microsoft` provider.** Multi-tenant (D-17.5).
- **oauth2-proxy straight to Google or Entra, no broker.** One provider only, and a separate CLI
  client per provider.
- **Group-based admission from Entra** (IdP mapper → Keycloak group → `groups` claim). Needs
  persistent users or per-login group sync; deferred, not taken.
- **SAML upstream providers.** Keycloak supports them, but the owner's providers all speak OIDC;
  deferred.

## 5. Tests

| ID | Test | Kind |
|---|---|---|
| T-17.1 | Keycloak and Dex together fail rendering; Keycloak with `allowedGroups` fails; Microsoft with `tenantId: common` fails schema | `verify-chart.sh` |
| T-17.2 | The rendered realm: both clients PKCE S256, standard flow only, exact redirects, no optional scopes, device grant off; brute-force on; lifetimes as D-17.4; no plaintext upstream secret anywhere in the render | `verify-chart.sh` |
| T-17.3 | Each upstream provider renders only when configured; Microsoft's issuer and endpoints name the tenant; its e-mail mapper reads `preferred_username` | `verify-chart.sh` |
| T-17.4 | oauth2-proxy's args with Keycloak: split endpoints, provider CA, CLI flags as D-15.6 | `verify-chart.sh` |
| T-17.5 | The Keycloak container: read-only root, non-root, no capabilities; mounts only its own certificate | `verify-chart.sh` |
| T-17.6 | The CLI probes Keycloak then Dex; a connection error stops the probe; `--issuer` skips it | CLI unit |
| T-17.7 | Signed in on kind through Keycloak with the demo user, the graph renders | E2E (`global-setup.ts`) |
| T-17.8 | Through nginx: `/keycloak/admin/`, `/keycloak/realms/master/…`, the realm's `account`, `clients-registrations` and device endpoints are `404` | kind |
| T-17.9 | Six wrong passwords lock the demo user out temporarily; an authorisation request for `topology-cli` without `code_challenge` is refused | kind |
| T-17.10 | `kubectl topology login` with no flags signs in through Keycloak; `graph` returns the demo edges | kind |
| T-17.11 | `/oauth2/sign_out` ends the session: the next API call is `401`, not a silent re-sign-in (D-17.10) | E2E (`auth.spec.ts`) |

## 6. Tracker

- [x] **K17-1** This ADR, the index, the plan entry, amendment notes in ADR-014 and ADR-015.
- [x] **K17-2** `keycloak/Dockerfile` (26.8.0 by digest, optimized, four features off),
  `make image-keycloak`, `make images` builds four, `make scan-images` covers it. Moved to 26.8.1
  on 2026-10-28, with Guava replaced by 33.7.2 to clear the scan gate (limitations §6.19); its first
  Keycloak scan had timed out, so `scan-images.sh` gives Trivy 15 minutes.
- [x] **K17-3** Chart: values, schema, realm, Deployment, Service, PKI, NetworkPolicy, oauth2-proxy
  args, NOTES, kind values — T-17.1 – T-17.5: `verify-chart.sh` 106/106. A mutation that drops PKCE
  from the clients or reads Entra's e-mail from `email` fails three checks.
- [x] **K17-4** Frontend nginx: `keycloak-{on,off}.conf`, `keycloak-proxy.conf`,
  `security-headers-keycloak.conf`, image env; `nginx -t` in both modes.
- [x] **K17-5** CLI issuer probe — T-17.6: five tests, `go test ./...` 105 passed.
- [x] **K17-6** Docs: operator guide (Google, Entra, other OIDC), limitations §6.13 – §6.18, demo
  script, README.
- [x] **K17-7** Verification on kind (Keycloak 26.8.0, kind v1.37): E2E 30 passed, 1 skipped (Grafana
  credentials, as before) — T-17.7, T-17.8, the PKCE half of T-17.9, T-17.11. T-17.9 lockout, by
  hand: six wrong passwords, then the correct one refused; accepted again about two minutes later.
  T-17.10: `kubectl topology login` with no flags chose `…/keycloak/realms/topology`, a one-hour ID
  token, and `graph --since 15m` listed the demo relationships. Three defects found on the way and
  fixed before this was ticked: sign-out did not end Keycloak's session (D-17.10); the CLI's
  `offline_access` request was refused, not ignored (D-17.4, corrected); the imported demo user
  lacked the default roles (D-17.4).
- [ ] **K17-8** A real Google and a real Entra ID sign-in. Needs app registrations the owner has not
  created yet; the realm, redirect URIs and pinning are verified by render only (T-17.3).
