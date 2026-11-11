# Key rotation

**Trigger:** the calendar, or suspected exposure of a key. Rotate the exposed key first, then check what it could have signed or unlocked.

| Key | Where it lives (proof of concept, production) | Rotation |
|---|---|---|
| LiteLLM virtual keys | gatekeeper memory and the LiteLLM database | Automatic: 24-hour expiry, rotated daily, deleted on revoke. Restarting gatekeeper issues fresh ones. |
| LiteLLM master key | secrets volume, Vault in production; only gatekeeper uses it | Generate a new key, update LiteLLM and gatekeeper together, restart both. No PC is involved because no PC ever holds it. |
| Online policy-signing key | secrets volume (`policy.key`), Vault transit in production | `policy-sign keygen <dir>` makes a new pair. The public key is built into the appliance image and installers, so ship it in a new release (see [release-rollout.md](release-rollout.md)); in v1 an offline root signs a monthly-rotating online key, so only the delegation changes. |
| Release signing key | secrets volume, offline HSM in production | New key; re-sign the manifests with `policy-sign manifest`; give the console the new public key (`RELEASE_PUBKEY_FILE`) and publish. |
| Device CA and appliance certificates | step-ca | Appliance certificates last one hour and renew by themselves. For a CA rotation, add the new root to the trust bundle in a release, then issue from the new intermediate. |
| step-ca provisioner password | secrets volume | Change it in step-ca, update the console, restart both. Enrollment and attestation fail while they differ. |
| Image signing (cosign) | CI | New key, re-sign sandbox images, update the verification key in the appliance image. |

Steps for any key: announce, rotate, publish new trust material, watch for verification errors (D3 alerts, `bundle rejected` in the manager log, `signature invalid`
in the console log), and keep the old trust material only as long as the overlap needs. Record the rotation in the incident or change ticket.
