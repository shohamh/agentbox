# Attestation failure on one PC (D3)

**Trigger:** alert D3 "Attestation failed: ..." for one device.

1. Console, Devices, open the PC: the evidence shows `reason`, `pcr11` and a `diff` (files that differ from the signed release manifest).
   Alerts shows the same evidence. The gateway already refuses the device; sandboxes keep running but model calls fail.
2. Read the reason:
   - *nonce unknown, reused or expired*: a stale or replayed attestation. Usually harmless if it happens once (clock skew, retry); the appliance re-attests on its own.
   - *quote signed by a key other than the enrolled attestation key*: the virtual TPM state changed. Treat as tamper (see [suspected-tampering.md](suspected-tampering.md)).
   - *PCR 11 matches no published production release*: compare the `diff` with Settings, Releases. A known new image that was not yet published? Publish its signed manifest.
     A file you do not recognize? Treat as tamper.
   - *appliance booted the dev flavor*: a dev image on a user PC. Never accepted; reinstall the production image.
   - *appliance release X has been revoked*: the PC must update; see [release-rollout.md](release-rollout.md).
3. If it is a known, benign cause (for example a mid-update PC), have the user run `agentbox recycle-appliance`; the appliance reboots and attests again.
   Otherwise leave the device failing and follow the tamper runbook.
4. Close the alert with a note (Alerts, close). If the device needs to be locked out explicitly, use Devices, revoke (reason required).
