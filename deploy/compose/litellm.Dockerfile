FROM ghcr.io/berriai/litellm:main-stable
COPY configs/control/litellm/config.yaml /app/agentbox-config.yaml
COPY configs/control/litellm/abx_callback.py /app/abx_callback.py
COPY configs/control/litellm/entrypoint.sh /app/agentbox-entrypoint.sh
RUN chmod 755 /app/agentbox-entrypoint.sh
ENV PYTHONPATH=/app LITELLM_LOG=WARNING DISABLE_SCHEMA_UPDATE=false
ENTRYPOINT ["/app/agentbox-entrypoint.sh"]
