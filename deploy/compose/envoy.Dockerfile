FROM envoyproxy/envoy:v1.36.2
COPY configs/control/envoy/front.yaml /etc/envoy/front.yaml
COPY configs/control/envoy/front-entrypoint.sh /usr/local/bin/front-entrypoint.sh
RUN chmod 755 /usr/local/bin/front-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/front-entrypoint.sh"]
