# The bundled Keycloak (ADR-017 D-17.2): the upstream image, pre-built ("optimized") so it starts
# with a read-only root filesystem.
#
# The stock image re-augments Quarkus into /opt/keycloak/lib on its first `start`, and again
# whenever a build-time option differs, so it cannot run read-only; `start --optimized` refuses an
# image that was never built. Building here moves that write to image-build time.

# Pinned by digest, in both stages, as every base image is (verify-image-pinning.sh).
FROM quay.io/keycloak/keycloak:26.8.0@sha256:27b3230fbabb8c9ff3c66c864cf09dcd8a41aca82e5100679d109ef0eeb05741 AS builder

# Build-time options only; everything else (hostname, TLS, cache, realm import) is given at run
# time by the chart — `cache` is a run-time option in 26.8 and a build does not persist it.
# dev-file: state is ephemeral and the realm is re-imported on every start (D-17.3). The disabled
# features are never used, and a feature that is not built in cannot be reached whatever nginx
# lets through (D-17.6).
ENV KC_DB=dev-file \
    KC_HEALTH_ENABLED=true \
    KC_HTTP_RELATIVE_PATH=/keycloak \
    KC_FEATURES_DISABLED=device-flow,ciba,impersonation,kerberos

RUN /opt/keycloak/bin/kc.sh build

FROM quay.io/keycloak/keycloak:26.8.0@sha256:27b3230fbabb8c9ff3c66c864cf09dcd8a41aca82e5100679d109ef0eeb05741

COPY --from=builder /opt/keycloak/ /opt/keycloak/

# The image's own non-root user (uid 1000); the chart also pins it.
USER 1000
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
CMD ["start", "--optimized"]
