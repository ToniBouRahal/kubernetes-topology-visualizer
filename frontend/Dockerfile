# Two-stage: build the SPA, then serve it from nginx with a same-origin API proxy.

FROM node:24-bookworm-slim@sha256:3638d9a6fe4030bd716be989438248074489337ba3275657f93595428be4fc03 AS builder

WORKDIR /build
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .
# The generated API client is committed, so this build only typechecks and bundles.
RUN npm run build

# ── Runtime ─────────────────────────────────────────────────────────────────────────────────
# nginx-unprivileged: runs as uid 101 and listens on 8080, so the pod needs no root and no
# NET_BIND_SERVICE capability.
FROM nginxinc/nginx-unprivileged:1.30-alpine@sha256:ed04ec1ff34502c339ee5c3ae3f855442398edc1d05591e2b98981dcbbd20b1e

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
    # Substitute ONLY BACKEND_*; nginx's own $host/$request_id/$uri must survive envsubst.
    # BACKEND_* plus the resolver the image derives from /etc/resolv.conf. nginx's own
    # $host/$uri/$backend variables must NOT be substituted.
    NGINX_ENVSUBST_FILTER="(BACKEND_|AUTH_MODE|DEX_|NGINX_LOCAL_RESOLVERS)"

COPY --from=builder /build/dist /usr/share/nginx/html
COPY default.conf.template /etc/nginx/templates/default.conf.template
COPY nginx/ /etc/nginx/snippets/
# Runs after the image's own envsubst step; see the script for why.
COPY 25-resolver.sh /docker-entrypoint.d/25-resolver.sh

EXPOSE 8080
