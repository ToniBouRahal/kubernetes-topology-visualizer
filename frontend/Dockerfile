# Two-stage: build the SPA, then serve it from nginx with a same-origin API proxy.

FROM node:24-bookworm-slim@sha256:55fb5f5a527ea2c5c4a95f963e48ef90aa38c7b346054fdc8ee9957a2b55d205 AS builder

WORKDIR /build
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .
# The generated API client is committed, so this build only typechecks and bundles.
RUN npm run build

# ── Runtime ─────────────────────────────────────────────────────────────────────────────────
# nginx-unprivileged: runs as uid 101 and listens on 8080, so the pod needs no root and no
# NET_BIND_SERVICE capability.
FROM nginxinc/nginx-unprivileged:1.30-alpine@sha256:f5116ed4606f3eb015dd4a8e28cb9b351476dc0ad4bc6e9f93b19cec130cdf03

# The newest 1.30-alpine digest still ships packages behind Alpine 3.24's own fixes (brotli-libs
# 1.2.0 and curl/libcurl 8.22.0, both HIGH, when this was added), so the installed packages are
# upgraded within that release, then the image's own unprivileged user is restored. Keep the step:
# it is what lets the image-scan gate pass between upstream rebuilds.
USER root
RUN apk upgrade --no-cache
USER 101

# Templated at start-up rather than baked in, so one image serves any release name.
# AUTH_MODE defaults to on (ADR-014 D-14.1): with no oauth2-proxy beside it, every request fails
# closed rather than serving the topology unauthenticated. The chart sets all of these.
ENV BACKEND_HOST=topology-topology-visualizer-backend \
    BACKEND_API_PORT=8444 \
    BACKEND_OPS_PORT=8000 \
    AUTH_MODE=on \
    DEX_MODE=off \
    DEX_HOST=topology-dex \
    DEX_PORT=5554 \
    KEYCLOAK_MODE=off \
    KEYCLOAK_HOST=topology-visualizer-keycloak \
    KEYCLOAK_PORT=8443 \
    # Substitute ONLY BACKEND_*; nginx's own $host/$request_id/$uri must survive envsubst.
    # BACKEND_* plus the resolver the image derives from /etc/resolv.conf. nginx's own
    # $host/$uri/$backend variables must NOT be substituted.
    NGINX_ENVSUBST_FILTER="(BACKEND_|AUTH_MODE|DEX_|KEYCLOAK_|NGINX_LOCAL_RESOLVERS)"

COPY --from=builder /build/dist /usr/share/nginx/html
COPY default.conf.template /etc/nginx/templates/default.conf.template
COPY nginx/ /etc/nginx/snippets/
# Runs after the image's own envsubst step; see the script for why.
COPY 25-resolver.sh /docker-entrypoint.d/25-resolver.sh

EXPOSE 8080
