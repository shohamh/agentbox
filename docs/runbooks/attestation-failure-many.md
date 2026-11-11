# Attestation failures across many PCs (D3 burst)

**Trigger:** several D3 alerts within minutes, or the Overview "devices attested" count dropping.

1. Do not revoke in bulk. Look for a common cause first:
   - a new Windows cumulative update or firmware rollout (the host verdict changed; in v1 the Rego policy checks properties, not PCR values);
   - a newly published or revoked appliance release (Settings, Releases: manifests and revoked releases);
   - a control-plane change: clock skew on the control plane (certificates and quotes depend on time), step-ca down, a wrong release public key.
2. Group the alerts in Alerts by `reason`. One reason across many PCs points at a policy or release problem, not at tampering.
3. Fix at the source:
   - missing manifest: publish the signed release manifest for the image that is actually deployed (drop the file in the releases directory; the console reloads it within 15 s).
   - policy too strict after an update (v1): update the Rego policy, run its tests, publish.
   - time: check `chrony` on the control-plane host and the appliance clocks; a clock more than five minutes off fails attestation.
4. For a time-boxed exception, document it in the incident, set an end time and re-enable strict handling afterwards. Never leave a bypass in place.
5. Verify recovery: Devices shows `pass` again for the affected PCs within one attestation interval; Overview "devices attested" is back to the previous count.
