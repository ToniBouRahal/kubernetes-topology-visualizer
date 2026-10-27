# The bundled Keycloak (ADR-017 D-17.2): the upstream image, pre-built ("optimized") so it starts
# with a read-only root filesystem.
#
# The stock image re-augments Quarkus into /opt/keycloak/lib on its first `start`, and again
# whenever a build-time option differs, so it cannot run read-only; `start --optimized` refuses an
# image that was never built. Building here moves that write to image-build time.

# Pinned by digest, in both stages, as every base image is (verify-image-pinning.sh).
FROM quay.io/keycloak/keycloak:26.8.1@sha256:a78d6b79572168cb25886b80a8b5b8d4ed4390743e3eecb810538683e209dd6b AS builder

# Build-time options only; everything else (hostname, TLS, cache, realm import) is given at run
# time by the chart — `cache` is a run-time option in 26.8 and a build does not persist it.
# dev-file: state is ephemeral and the realm is re-imported on every start (D-17.3). The disabled
# features are never used, and a feature that is not built in cannot be reached whatever nginx
# lets through (D-17.6).
ENV KC_DB=dev-file \
    KC_HEALTH_ENABLED=true \
    KC_HTTP_RELATIVE_PATH=/keycloak \
    KC_FEATURES_DISABLED=device-flow,ciba,impersonation,kerberos

# TEMPORARY: Guava 33.6.0, as 26.8.1 ships it, has a fixable HIGH (CVE-2026-102554, unthrottled
# memory allocation) that fails the image-scan gate. 33.7.2 is a compatible minor release; it is
# written over the shipped jar under the SAME file name, because Quarkus's fast-jar layout lists
# its classpath by file name. Checksum from Maven Central, verified by the build. Delete this
# step once a Keycloak release ships Guava >= 33.7.2. Verified on kind: limitations.md §6.19.
ADD --chown=1000:0 --checksum=sha256:b530942257fb935f8b2cfaa5f8eb5bd59c555fd8e8d01b8ce98912e077ea606c \
    https://repo1.maven.org/maven2/com/google/guava/guava/33.7.2-jre/guava-33.7.2-jre.jar \
    /opt/keycloak/lib/lib/main/com.google.guava.guava-33.6.0-jre.jar

RUN /opt/keycloak/bin/kc.sh build

FROM quay.io/keycloak/keycloak:26.8.1@sha256:a78d6b79572168cb25886b80a8b5b8d4ed4390743e3eecb810538683e209dd6b

COPY --from=builder /opt/keycloak/ /opt/keycloak/

# The image's own non-root user (uid 1000); the chart also pins it.
USER 1000
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
CMD ["start", "--optimized"]
