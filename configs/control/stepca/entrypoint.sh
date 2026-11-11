#!/bin/sh
# Initializes step-ca on first start with a JWK provisioner named "agentbox"
# (password from the secrets volume) and long-enough certificate lifetimes for
# device certificates, then runs the CA.
set -e
export STEPPATH=/home/step
PW=/secrets/stepca_password
if [ ! -f "$STEPPATH/config/ca.json" ]; then
  echo "initializing step-ca"
  step ca init --deployment-type=standalone --name="Agentbox" \
    --dns=ca.agentbox.corp --dns=stepca --dns=localhost --address=:9000 \
    --provisioner=agentbox --password-file="$PW" --provisioner-password-file="$PW"
  step ca provisioner update agentbox --ca-config="$STEPPATH/config/ca.json" \
    --x509-min-dur=5m --x509-max-dur=2160h --x509-default-dur=24h
fi
exec step-ca "$STEPPATH/config/ca.json" --password-file="$PW"
