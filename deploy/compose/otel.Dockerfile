FROM otel/opentelemetry-collector-contrib:0.138.0 AS src
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl coreutils \
    && rm -rf /var/lib/apt/lists/*
COPY --from=src /otelcol-contrib /otelcol-contrib
COPY configs/control/otel/base.yaml /etc/otelcol/base.yaml
COPY configs/control/otel/run.sh /run.sh
RUN chmod 755 /run.sh
ENTRYPOINT ["/run.sh"]
