# Appliance release rollout

**Trigger:** a release is approved and signed in the signing ceremony.

1. **Publish the manifest first.** The release build writes a signed manifest (`agentbox-measure manifest` piped through `policy-sign manifest <release-key> <out>`)
   with the expected PCR 11 and the file hashes. Put it in the console's releases directory. Settings, Releases lists it within 15 s;
   a manifest with a bad signature is ignored and logged. The verifiers must know a release before any PC is offered the image.
2. **Canary** (platform team and security): set the channel for those devices (setting `release_channel:<device>`); wait 48 hours without new alerts or crash reports.
3. **Early** (volunteers, about 10% of R&D): one week with error rates no worse than the current release.
4. **Broad**: everyone. Devices shows the version spread (`appliance_version` per device); both the current and the previous release are accepted at once, so no flag day.
5. **Roll back by channel** if error rates rise: move the channel back; PCs recycle onto the previous release and attest against its manifest.
6. **Revoke a bad release**: Settings, `revoked_releases`, add the version (audited). Devices still on it fail attestation (`appliance release X has been revoked`) and
   lose model access until they update; sandboxes keep running.
7. Watch D3 alerts and the Overview "devices attested" count during the rollout; a spike means a manifest or image mismatch (see [attestation-failure-many.md](attestation-failure-many.md)).
