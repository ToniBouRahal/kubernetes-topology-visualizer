# Two-stage: build the SPA, then serve it from nginx with a same-origin API proxy.

FROM node:24-bookworm-slim AS builder

WORKDIR /build
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .
# The generated API client is committed, so this build only typechecks and bundles.
RUN npm run build

# ── Runtime ─────────────────────────────────────────────────────────────────────────────────
# nginx-unprivileged: runs as uid 101 and listens on 8080, so the pod needs no root and no
# NET_BIND_SERVICE capability.
FROM nginxinc/nginx-unprivileged:1.27-alpine

# Templated at start-up rather than baked in, so one image serves any release name.
ENV BACKEND_HOST=topology-topology-visualizer-backend \
    BACKEND_PORT=8000 \
    # Substitute ONLY BACKEND_*; nginx's own $host/$request_id/$uri must survive envsubst.
    # BACKEND_* plus the resolver the image derives from /etc/resolv.conf. nginx's own
    # $host/$uri/$backend variables must NOT be substituted.
    NGINX_ENVSUBST_FILTER="(BACKEND_|NGINX_LOCAL_RESOLVERS)"

COPY --from=builder /build/dist /usr/share/nginx/html
COPY default.conf.template /etc/nginx/templates/default.conf.template

EXPOSE 8080
