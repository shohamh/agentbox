FROM envoyproxy/envoy:v1.36.2
COPY configs/control/envoy/front.yaml /etc/envoy/front.yaml
CMD ["-c", "/etc/envoy/front.yaml", "--log-level", "warn"]
