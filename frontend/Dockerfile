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
FROM nginxinc/nginx-unprivileged:1.27-alpine@sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0

# Templated at start-up rather than baked in, so one image serves any release name.
ENV BACKEND_HOST=topology-topology-visualizer-backend \
    BACKEND_PORT=8000 \
    # Substitute ONLY BACKEND_*; nginx's own $host/$request_id/$uri must survive envsubst.
    # BACKEND_* plus the resolver the image derives from /etc/resolv.conf. nginx's own
    # $host/$uri/$backend variables must NOT be substituted.
    NGINX_ENVSUBST_FILTER="(BACKEND_|NGINX_LOCAL_RESOLVERS)"

COPY --from=builder /build/dist /usr/share/nginx/html
COPY default.conf.template /etc/nginx/templates/default.conf.template
# Runs after the image's own envsubst step; see the script for why.
COPY 25-resolver.sh /docker-entrypoint.d/25-resolver.sh

EXPOSE 8080
