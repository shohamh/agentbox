# Relay attempt (D4, D5)

**Trigger:** D4 "Sandbox token used from X instead of its own sandbox", or D5 "Model-API-shaped request blocked on preview port".

What happened: someone copied a sandbox token out of its sandbox and used it from the host, another sandbox or another PC (D4), or ran
something shaped like a model API behind a preview port (D5). Both were refused; the alert shows the source address, sandbox and path.

1. Console, Alerts: open the alert. Note the user, device, sandbox and the source of the attempt.
2. Activity: open the sandbox's timeline around the alert (events `token.relay`, `preview.blocked`, `process.exec`, `net.connect`).
   Look for a model-API proxy process, a tunnel or a port forward set up shortly before.
3. Talk to the user. Accidental causes: a script that exports `AGENTBOX_TOKEN` to another tool, a local dev server that calls `llm.agentbox`.
4. Act by policy:
   - first offence, explained: acknowledge the alert (Alerts, acknowledge) and note the explanation;
   - repeated or unexplained: Users, suspend the user (reason required), or Devices, revoke. Remove a flagged permission such as `port:<n>` from Requests, rollback.
5. The token is worthless outside its sandbox, and no real credential exists on any PC, so no key rotation is needed. Recycle the sandbox (Sandboxes, recycle) to issue a fresh token if the user needs to continue.

Automated checks: E2E-06, T9 and T10.
