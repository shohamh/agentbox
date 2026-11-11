# Suspected model backdoor trigger

**Trigger:** D6 (unexplained execution) or D7 (denied egress) alerts clustering across several users on the same model.

1. Console, Alerts: group by detection and model. A cluster of D6 or D7 across unrelated users within the same time window on one model is the signal.
2. **Switch the model off.** Kill switches, toggle `model:<name>` (reason required). Calls to that model fail for everyone within 30 s; other models keep working.
   Sandboxes keep running. Use the global switch only for an incident that spans models; it needs a second admin to confirm.
3. **Preserve evidence.** Activity: for each affected sandbox, export the timeline and pin the model call and the process or network events that followed it.
   Prompt and response bodies stay available for 30 days to the investigator role.
4. Notify the model-intake owner. Compare behaviour on the same prompts with another model family, using the reviewer or a test sandbox.
5. Keep the switch on until the model is cleared or replaced. Turning it off needs a reason and is audited.
6. After the incident, review the egress catalog (Policy) for any host the affected sandboxes tried to reach.
