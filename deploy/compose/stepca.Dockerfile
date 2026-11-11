FROM smallstep/step-ca:0.28.4
USER root
COPY configs/control/stepca/entrypoint.sh /entrypoint-agentbox.sh
RUN chmod 755 /entrypoint-agentbox.sh
USER step
ENTRYPOINT ["/entrypoint-agentbox.sh"]
