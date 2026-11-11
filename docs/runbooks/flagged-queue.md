# Flagged-change queue (daily)

Flagged permission changes are applied at once and then checked by a reviewer model from a different family. They expire after 14 days unless renewed.

1. Console, Requests: filter by `flagged`. Work oldest first.
2. For each item read the reviewer's verdict and reason (`consistent`, `unusual`, `escalate`) next to the user's stated reason, team and recent grants.
   - `consistent`: closed by the reviewer; 5% are sampled (`sampled`) for a human to look at.
   - `unusual` or `escalate`: also raise a D10 alert to the approver queue. `escalate` includes any reviewer output that did not parse (fail closed).
3. Decide:
   - **Confirm** (approver or admin): keep it; the item is marked confirmed.
   - **Roll back**: the grant is removed from the user's next bundle; the PC drops it within seconds.
   - Ask the user when unsure; the stated reason and the audit trail are on the request.
4. **Turn repeated safe patterns into automatic rules** (admin: Requests, rule): the pattern, for example `egress:artifacts.agentbox.corp`, then applies instantly without review. Rules are audited.
5. Close the daily check by emptying the unreviewed list; the Overview "requests flagged" counter should reach zero.
