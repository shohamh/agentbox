# Destructive activity (D8)

**Trigger:** D8 "Mass delete, rename or overwrite in a mount". D8 arrives with the v1 correlator and host mounts; in the proof of concept
use this runbook when a user reports lost files or the timeline shows unexpected mass deletes.

1. The sandbox should already be paused. If not: console, Sandboxes, stop the sandbox (reason required). A stopped sandbox keeps its workspace layer; do not `rm` it yet.
2. Activity: open the sandbox timeline and read the model calls and tool calls just before the damage. Check whether the commands match what the user asked for
   (an agent misreading a task, or a model steering it).
3. Review the agent transcript with the user. If the model looks at fault, see [model-backdoor.md](model-backdoor.md).
4. Restore: from the PC's own backups or from Git for synced project folders; the sandbox's own workspace layer is on the appliance data disk.
5. Narrow the blast radius before the user continues: remove or narrow the mount in the group policy (Policy), or move the work to a copy of the project.
6. Close the alert with the cause and the restore.
