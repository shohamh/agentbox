FROM grafana/grafana:12.2.0
USER root
RUN grafana cli plugins install grafana-clickhouse-datasource
COPY configs/control/grafana/provisioning /etc/grafana/provisioning
COPY configs/control/grafana/dashboards /var/lib/agentbox-dashboards
RUN chown -R 472:0 /var/lib/grafana /etc/grafana/provisioning /var/lib/agentbox-dashboards
USER 472
ENV GF_AUTH_ANONYMOUS_ENABLED=true GF_AUTH_ANONYMOUS_ORG_ROLE=Viewer GF_ANALYTICS_REPORTING_ENABLED=false \
    GF_ANALYTICS_CHECK_FOR_UPDATES=false GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=grafana-clickhouse-datasource
