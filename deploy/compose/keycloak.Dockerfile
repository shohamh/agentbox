FROM quay.io/keycloak/keycloak:26.4 AS build
ENV KC_HEALTH_ENABLED=true KC_METRICS_ENABLED=false KC_DB=dev-file KC_CACHE=local
RUN /opt/keycloak/bin/kc.sh build

FROM quay.io/keycloak/keycloak:26.4
COPY --from=build /opt/keycloak/ /opt/keycloak/
COPY configs/control/keycloak/agentbox-realm.json /opt/keycloak/data/import/agentbox-realm.json
# Pre-built (optimized) so startup needs little memory; realm is imported on start.
ENV JAVA_OPTS_KC_HEAP="-Xms64m -Xmx300m -XX:MaxMetaspaceSize=200m"
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
CMD ["start", "--optimized", "--import-realm", "--http-enabled=true", "--http-port=8080", \
     "--hostname=http://kc.agentbox.corp:8080", "--hostname-strict=false"]
