# Suspected tampering (D1, D2, tamper-matrix hits)

**Trigger:** D1 (host process opening the Hyper-V worker or VM memory), D2 (ptrace or /proc/pid/mem against QEMU or swtpm), an attestation failure that points at modified image files, or a T-matrix hit.
D1 and D2 come from the v1 host sensors; in the proof of concept the evidence is the attestation diff and the D3 alert.

1. **Confirm the device is locked out.** Console, Devices: attestation `fail` or `revoked`. If it is not revoked, revoke it now (reason required).
   Model calls fail within 30 s; the gateway deletes the device's LiteLLM virtual keys.
2. **Preserve evidence.** Alerts: open the alert and its linked evidence. Activity: filter by the device's user and sandboxes, export the timeline (CSV), and pin the key events as evidence.
   Collect host logs through Wazuh (Sysmon on Windows, auditd on Linux) for the same window.
3. **Stop the appliance's sandboxes if needed.** Sandboxes, stop (reason required). Their workspaces stay on the data disk.
4. **Talk to the user** before concluding: updates, VMware or VirtualBox conflicts and debugging sessions cause false positives.
5. **Decide.** Benign: re-enroll after review (`agentbox enroll`), which resets the device to pending and restores access once attestation passes.
   Malicious or unexplained: keep the device revoked, involve the security team, and re-image before any re-enrollment.
6. Close the alert with the outcome. The revoke, the notes and the re-enroll are in Audit.

Automated tamper tests for the proof of concept: `scripts/test.sh` with `TEST=TAMPER` (T1, T2, T3, T7, T9, T10, T12).
