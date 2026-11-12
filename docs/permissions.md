# Permissions

How a developer gets more than the default, who decides, and what each permission
really changes. For developers, approvers and administrators. The mechanism is the
one in the build plan ("Policy bundles, permission service and reviewer"); this page
says what is built and tested.

## The idea

Everything a sandbox may do is written in one **signed policy bundle per device**:
which images, which models, which hosts it may reach, which ports it may forward,
which tier. The permission service is the only writer. The appliance verifies the
signature and is the only enforcer. A user asks for more with one command:

```bash
agentbox allow egress:github.com --reason "release downloads for the formatter"
agentbox allow image:omp --reason "trying a second agent"
agentbox allow model:small --reason "cheap model for the log summarizer"
agentbox allow port:8080 --reason "debug the service in my sandbox from my IDE"
agentbox perms                      # your requests and their state
```

A reason is mandatory and is recorded. Asking again for something you already hold
does not create a duplicate (see Renewal).

## Three outcomes

| Class | What happens | Typical |
|---|---|---|
| **automatic** (`instant`) | Applied at once, listed as automatic | hosts on the pre-approved list (package mirrors), an image your group already has, a model your group already has |
| **flagged** | Applied at once, lasts 14 days, and a reviewer model from a different family judges whether it fits the user's role. "Consistent" closes it (5 % are sampled for a human); anything else, or an answer that cannot be parsed, goes to the approvers' queue where a person keeps, shortens or rolls it back | lab hosts, a raw TCP port |
| **approval** | Waits for an approver in the console (role: approver). The CLI waits and tells you who decided and why | a host nobody listed, a model or an image outside your group, a higher tier |
| **never** | Refused at once with the reason | the public model APIs, the model endpoint and DNS ports, a model the platform does not serve, a tier above the group's cap |

## What each kind does

| Permission | Enforced by | Effect |
|---|---|---|
| `egress:<host>` | the appliance's egress proxy and DNS allowlist | the sandbox can reach the host (`*.lab.example` patterns work in the catalog) |
| `image:<name>` | the appliance when a sandbox is created | `agentbox new --image <name>` works. Names not in the catalog are refused |
| `model:<name>` | the gateway: the device's models in the signed bundle, and the LiteLLM key minted for it | the model answers. Names LiteLLM does not serve (the catalog) are refused |
| `port:<n>` | the appliance's TCP tunnel | `agentbox forward <sandbox> <n>` opens a raw TCP forward. Web previews (`agentbox preview`) need no permission but are inspected so they cannot carry model-API traffic |
| `tier:<n>` | the gateway: the tier in the bundle is checked against the device's assurance level, and stamped on the model keys | a tier the device's assurance level cannot carry is refused at the gateway with a message that says so |
| `gui:<kind>` | the appliance (desktop feature) | display channels from a sandbox to the PC |
| `mount:<path>` | not available in this version | the request is refused with an explanation: sandboxes work on a clone of the repository (`agentbox new <repo>`) and changes come back with `git fetch` over the sandbox's SSH entry. Folder sync is not built |

## Deciding (approvers and administrators)

Console, page **Requests**. Each row shows who asked, what, why, the class, the
reviewer model's verdict and reasoning for flagged ones.

- **Approve**, optionally **with an expiry** (hours). A time-boxed grant stops applying
  within seconds of its expiry (the permission service checks every 15 seconds and
  signs a new bundle at once), and the request shows as *expired*.
- **Deny** with a reason; the CLI prints it.
- **Roll back** a grant that is active: it stops applying in seconds.
- **Confirm** a flagged grant after review.
- **Make it a rule** (administrators only): a pattern such as `egress:*.example.org`
  becomes automatic for everyone from then on. An approver cannot create rules.

Every request, decision and change is in the audit log (page **Audit**) with the actor,
and in the telemetry store as an `audit.*` record.

## Renewal and expiry

Flagged grants last 14 days. Renew with the same command; it extends the existing
grant instead of adding another:

```
$ agentbox allow egress:x.lab.example --reason "still needed"
egress:x.lab.example: active (flagged) - renewed for 14 days
```

A grant an approver time-boxed is not extended by asking again: that needs a new
approval. Automatic grants have no expiry.

## When the control plane is down

Sandboxes keep running with the last bundle. A bundle lasts 24 hours and is
re-signed well before that, so an outage shorter than a day changes nothing.
After a bundle expires: **model calls stop at once** (the gateway also needs the
control plane), and **everything that is not a model call**, egress and ports and
images, **keeps working for another 72 hours**. After that the sandbox keeps running
but has no network until the appliance reaches the control plane again.
`agentbox allow` itself needs the control plane and says so plainly when it cannot reach it.

## Where it is tested

E2E-21 to E2E-27 ([testing.md](testing.md)) exercise each permission kind through the
real CLI, the real console API and the real appliance: image, model, port (with data
flowing through the forward), tier (enforced at the gateway), mount (refused),
expiry (a grant ends within 45 seconds of its time box), renewal, and an
administrator's rule. UI-01 to UI-03 and UI-12 cover the console screens and roles.
