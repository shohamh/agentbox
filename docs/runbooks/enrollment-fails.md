# Enrollment fails

**Trigger:** a user reports `agentbox enroll` failing, or the console Audit page shows `device.enroll_refused`.

1. Ask the user for the exact message from `agentbox enroll`, and run `agentbox doctor` on their PC.
2. Match the message:
   - *"device X is not in the Fleet inventory; ask IT to register it"*: the device ID is not in the inventory. Check the
     inventory (Fleet in production; the `inventory` table in the proof of concept). Add the device with its assigned user, then retry.
   - *"device X is assigned to another user in the inventory"*: the signed-in user is not the assigned user. Fix the assignment or have the right user sign in.
     The refusal is in Audit as `device.enroll_refused` with the actual assignee.
   - *"sign-in failed" / "sign-in service unavailable"*: check Keycloak (`docker compose -p agentbox logs keycloak`), the user's directory account, and MFA at enrollment.
   - *"control plane unreachable"*: the PC cannot reach `enroll.agentbox.corp`; check the corporate network and DNS from the PC.
   - *"certificate authority: ..."*: step-ca is down or its provisioner password changed; check `docker compose logs stepca` and the console log.
   - *"the appliance is not reachable"*: the appliance VM is not running; `agentbox status` shows the failed prerequisite and its fix
     (Hyper-V off, memory integrity off, KVM or TPM missing).
3. Re-run `agentbox enroll`. Enrolling again is safe: it replaces the device record and resets attestation to pending.
4. Confirm in the console: Devices shows the PC, attestation `pass` within 60 s, and a bundle version.

**Escalate** if the inventory, sign-in and CA are healthy and the failure persists: collect `agentbox doctor` output and the console log around the `/api/v1/enroll` call.
