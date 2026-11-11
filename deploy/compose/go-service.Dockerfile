# One image per Go binary: docker build --build-arg BIN=gatekeeper ...
# Needs agentbox-bins:dev (scripts/build-bins.sh) and agentbox-base:dev (scripts/bootstrap.sh).
FROM agentbox-base:dev
ARG BIN
COPY --from=agentbox-bins:dev /bin/${BIN} /usr/local/bin/svc
ENTRYPOINT ["/usr/local/bin/svc"]
