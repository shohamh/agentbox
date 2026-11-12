# Using Agentbox: a guide for developers

Agentbox lets you run AI coding agents (Claude Code, Codex, opencode, omp, and
others) on your own PC without giving them your PC. Each agent works in a
**sandbox**: a locked-down Linux environment with its own copy of your project,
no access to your files, your credentials or the rest of the network, and one
way to reach a model: through the company's model service. You never see or
handle a model key. Nothing you do in a sandbox can reach anything but the
project and the services your administrators allow.

## The 2-minute start

```bash
agentbox enroll                       # once per PC: sign in with your company account
agentbox status                       # every line should be ok; it says what to fix if not
agentbox new https://git.agentbox.corp/team/project --name work --image claude-code
agentbox shell work                   # a terminal inside the sandbox, in /workspace
claude                                # (or: codex, opencode, omp: see "Which agent")
```

Windows: the same commands in PowerShell (`agentbox.exe`). On a Windows PC the
sandbox runs in a small Hyper-V virtual machine that Agentbox manages; you never
open it.

`agentbox new <repo>` clones the repository into the sandbox's `/workspace`. Leave
the repository out to start from an empty project. Only repositories on hosts your
administrators allow can be cloned (`git.agentbox.corp` by default).

## Which agent

| Image (`--image`) | Start it with | Notes |
|---|---|---|
| `claude-code` | `claude` | Claude Code. Interactive (`claude`) or one-shot (`claude -p "task"`). Runs with permission prompts off: the sandbox is the boundary. |
| `codex` | `codex` / `codex exec "task"` | OpenAI Codex CLI. |
| `opencode` | `opencode` / `opencode run "task"` | opencode. |
| `omp` | `omp` / `omp -p "task"` | Oh My Pi. Needs nothing configured: the model list comes from your policy. |
| `base` | whatever you install | Git, Python, build tools and nothing else: for a tool Agentbox does not ship (below). |

The agent already points at the company model service and uses a model your
policy allows. `echo $AGENTBOX_MODELS` lists them (the first one is the default).
To use another allowed model, tell the agent (for example `claude --model <name>`,
`omp --model agentbox/<name>`, `/model` inside the tool).

`agentbox list` shows your sandboxes. `agentbox stop <name>` / `start <name>`
pause and resume one (your files stay); `agentbox rm <name>` deletes it and its
files; `agentbox recycle <name>` stops and starts it again (use it after your administrators change the model list).

## Editors and IDEs

```bash
agentbox code work         # VS Code: opens the sandbox with Remote-SSH
```

`agentbox code` writes an SSH entry, `agentbox-work`, to `~/.ssh/agentbox_config`.
Add this line once at the top of `~/.ssh/config` (on Windows
`C:\Users\<you>\.ssh\config`):

```
Include agentbox_config
```

Anything that speaks SSH can then use the host `agentbox-work`: VS Code
Remote-SSH, JetBrains Gateway, plain `ssh agentbox-work`, `scp`, `rsync`.

### Orca (and other agent managers)

Orca runs many agents side by side, each in its own git worktree, and can run them
on a remote machine over SSH. Use the SSH host above as that remote machine, so
the agents Orca starts run **inside the sandbox**:

1. `agentbox new <repo> --name work --image claude-code` and `agentbox code work` once.
2. In Orca, add an SSH remote for host `agentbox-work` (it is read from your SSH config).
3. Open the project from the remote and start Claude Code, Codex, opencode or omp there.

Do not start agents in Orca's **local** terminals for company code: those run on
your PC with your files and your own credentials, outside Agentbox, and the
company model service will not serve them. The SSH path (OpenSSH on Windows and Git's
ssh) is tested; Orca itself is not part of Agentbox's test suite, so tell your
administrators if Orca cannot use the entry.

### Previewing what an agent builds

```bash
agentbox preview work 3000            # http://localhost:3000 on your PC shows port 3000 of the sandbox
```
agentbox forward work 5432            # raw TCP (a database, a debugger): needs `agentbox allow port:5432 --reason "..."`

## Another agent, or a tool of your own

Any tool that talks to an OpenAI-style or Anthropic-style API works, in any
sandbox, because the environment is already set up:

| Variable | Value |
|---|---|
| `OPENAI_BASE_URL`, `OPENAI_API_KEY` | the model service (`…/v1`) and your sandbox token |
| `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY` | the same for tools that speak Anthropic's Messages API |
| `AGENTBOX_TOKEN` | your sandbox token (a local token, not a model key) |
| `AGENTBOX_MODEL`, `AGENTBOX_SMALL_MODEL`, `AGENTBOX_MODELS` | the default model, a small fast one, and everything allowed |

Use `AGENTBOX_MODEL` wherever the tool asks for a model name: a name that is not
on the list is refused. A tool that insists on a provider name (some ask for
`gpt-4o`, `claude-sonnet-4`) needs the allowed name configured in its settings.

Installing the tool: the sandbox can reach the company package mirrors
(`pypi.agentbox.corp`, `npm.agentbox.corp`, …) and nothing else on the internet, so
`pip install` and `npm install` work when they use those mirrors:

```bash
pip install --index-url https://pypi.agentbox.corp/simple <package>
npm install -g --registry https://npm.agentbox.corp <package>
```

If your tool needs something else (another host, a binary release), ask for it:

```bash
agentbox allow egress:github.com --reason "Release downloads for the formatter we use"
agentbox allow image:my-tool --reason "Our own agent image"
agentbox perms                        # the state of your requests
```

Hosts on the company's pre-approved list are approved at once, others wait for an
approver, and some hosts (the public model APIs, for example) are never allowed.
If your team uses an agent a lot, ask your administrators for an image for it
(they add a folder under `sandbox-images/`; `claude-code` and `omp` are the
examples).

## Getting your work out

The sandbox is disposable; your repository is not. Commit inside the sandbox,
then fetch the commits to your PC over the same SSH entry:

```bash
git remote add work agentbox-work:/workspace      # once, in your local clone
git fetch work && git merge work/main             # or cherry-pick what you want
```

or copy files with `scp agentbox-work:/workspace/file .` or VS Code. Stopping a
sandbox keeps its files; removing it deletes them.

## What you will see when something is refused

The service tells you why, in plain words:

| Message | What it means | What to do |
|---|---|---|
| `model X is not in your policy (allowed: …)` | the tool asked for a model your group may not use | use one from the list, or `agentbox allow model:<name> --reason "…"` (an approver decides) |
| `model budget exhausted for you: $x used of $y` | your spend limit is reached | ask your administrator to raise it |
| `all model access is paused by an administrator (…)` / `model X is switched off …` | a kill switch is on | wait: the reason is in the message |
| `appliance attestation is …; model access is off until it passes` | this PC's agent platform failed its integrity check | `agentbox status`, then `agentbox doctor`; contact IT |
| `device … was revoked` | an administrator revoked this PC | contact IT; you re-enroll after review |
agentbox allow model:small --reason "A cheap model for the log summarizer"
agentbox allow port:5432 --reason "Debug the database in my sandbox from my IDE"
| `model service unreachable, try again shortly` | the company model service is down or unreachable | try again; running work continues, `agentbox status` shows the layer that is down |
| `image "x" is not in your policy` | the image is not allowed for you | `agentbox allow image:x --reason "…"` |

`agentbox status` shows every layer with a fix for the one that is wrong.
`agentbox doctor` prints what support needs (no secrets in it).
Everything about permissions, including what each kind changes, expiry and renewal,
is in [permissions.md](permissions.md).

## What is recorded

Be aware of this, because it is the point of the design: the company sees every
model request from your sandboxes (who, which device and sandbox, which model,
tokens, cost), every tool call the model asks for and what actually ran, the
prompts you type into the agents that log them (Codex and Claude Code do), the
commands that run, and every network connection the sandbox makes. It is
visible to administrators and the security team, and it stays inside the company. Do not paste personal secrets or
other people's private data into a sandbox.

## Good habits

- One sandbox per project or task; remove it when done.
- Keep long-lived secrets out of the repository and the sandbox. The sandbox has no
  access to your PC's credentials by design.
- If an agent runs a command you did not expect, stop the sandbox
  (`agentbox stop work`) and tell security: the commands are recorded either way.
- A sandbox cannot see your PC's files. Put what it needs in the repository.

## Troubleshooting

| Problem | Try |
|---|---|
| `agentbox status` shows the appliance not running | `agentbox up`, then wait a minute; `agentbox doctor` if it stays red |
| `agentbox new` is slow | the first sandbox of an image downloads it (a minute or two); later ones start in seconds |
| ssh asks for a password | you used `ssh` on a sandbox without the entry: run `agentbox code <name>` and add the `Include` line, or use `agentbox shell <name>` |
| An agent says it cannot reach the model | `agentbox status`; if it is all ok, the model name is wrong: `echo $AGENTBOX_MODELS` |
| Claude Code warns it does not recognise the model | harmless for models with a 200k window |
| `pip` or `npm` cannot fetch a package | the package mirror may not have it: request it from your administrators, or `agentbox allow egress:<host> --reason "…"` |
