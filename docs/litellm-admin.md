# LiteLLM guide for the maintainers

For the people who run the company's LiteLLM proxy. It says what Agentbox needs
from the proxy, how to connect it, how to check it, and how to run it day to day.
Nobody on your team has to learn Agentbox internals: Agentbox treats LiteLLM as
the one place models are configured, priced and metered, and never changes it
except to create and delete its own short-lived keys.

Tested with LiteLLM 1.109.1 (`ghcr.io/berriai/litellm:main-stable` at the time of
writing). Run `scripts/preflight.sh` (below) after every upgrade.

## 1. How a model call is authorised

```
sandbox ──(local token)──> appliance Envoy ──(mTLS, appliance certificate)──> front Envoy ──> gatekeeper ──┐
                                                                                                          │ checks, then attaches a LiteLLM key
LiteLLM <──────────────────────── request with  Authorization: Bearer <short-lived key>  ─────────────────┘
```

- The **sandbox** holds one secret, a local token (`abx-sbx-…`) that only its own
  appliance accepts. It is not a LiteLLM key and LiteLLM rejects it.
- The **gatekeeper** is the only holder of the LiteLLM master key. For every
  request it checks, in order: the appliance certificate (step-ca, one hour), the
  device is enrolled and belongs to that user, is not revoked, **attestation is
  `pass`**, the user is not suspended, no kill switch applies (global, group or
  model), the model is in the device's signed policy, the user's tier fits the
  device's assurance level, and the user has budget left. Only then does it attach
  a key. A request that fails any check never gets a key.
- The key it attaches is minted for **one sandbox**: `POST /key/generate` with

  | field | value |
  |---|---|
  | `duration` | `KEY_TTL` in seconds (default 15 minutes) |
  | `models` | exactly the models of that device's current policy |
  | `user_id` | the user (so LiteLLM spend adds up per person) |
  | `key_alias` | `abx:<user>:<device>:<sandbox>:t<tier>:<unix time>` |
  | `metadata` | `agentbox.user`, `.device`, `.sandbox`, `.tier`, `.minted_by: gatekeeper` |
  | `max_budget` | what is left of the user's budget when the key is minted |
  | `rpm_limit`, `tpm_limit`, `max_parallel_requests` | from `KEY_RPM`, `KEY_TPM`, `KEY_PARALLEL` when set |

- A key is replaced when a third of its life is left (`KEY_RENEW_BEFORE`), so a
  busy sandbox sees a new key every 10 minutes at the default. The old one is left
  to expire (a request already in flight keeps working) and is deleted a minute
  after it expires, so LiteLLM's key table does not grow.
- **Revocation is immediate.** When a device is revoked in the console, or its
  attestation stops passing, the gatekeeper deletes its keys within a couple of
  seconds (measured 1.3 s in E2E-19) and mints no new ones.

What this means for you: no person and no sandbox ever holds a LiteLLM key; a
leaked local token is useless outside its own appliance; and a key that does leak
from gatekeeper memory is good for minutes, for one sandbox, for that device's
models, up to the user's remaining budget.

## 2. What the proxy must provide

| Need | Why |
|---|---|
| A Postgres database (`general_settings.database_url`) | virtual keys, expiry and spend live there; `allow_requests_on_db_unavailable: false` |
| A master key (or the key of a `proxy_admin` user) | the gatekeeper creates and deletes keys with it |
| Every model under the **name callers use** | Agentbox puts these names in the sandboxes (see §4) |
| `/v1/chat/completions`, `/v1/messages`, `/v1/responses`, all streaming | opencode and omp use chat completions, Claude Code uses the Messages API, Codex uses Responses |
| The callback `abx_callback.py` | one `model.call` event per request and one `gen_ai.tool_call` per tool call, stamped with user, device and sandbox; the console and the correlator need them |
| Prices on self-hosted models (`input_cost_per_token`, `output_cost_per_token`) | without a price, spend stays at zero and budgets never trip |
| Reachability **only** from the gateway, from your operators, and to the telemetry gateway | see §6 |

`configs/control/litellm/config.prod.example.yaml` is a complete starting point
(Anthropic, vLLM, Azure OpenAI and Bedrock entries, and the settings above).

## 3. Connect it

1. **Models.** Add each model to `model_list`. Alias freely (`claude-sonnet`,
   `small`, `code-default`): callers never see the provider's name. For a model
   you host, set the prices.
2. **Callback.** Put `configs/control/litellm/abx_callback.py` on the proxy's
   `PYTHONPATH` and set `litellm_settings.callbacks: abx_callback.handler`. Give
   the proxy `OTLP_ENDPOINT=http://<telemetry gateway>:4319/v1/logs`.
3. **Master key.** Write it to a file the gatekeeper can read
   (`LITELLM_MASTER_KEY_FILE`, mounted from your secret store). Nothing else in
   Agentbox needs it.
4. **Check it** (next section) until every line says `ok`.
5. **Point Agentbox at it.** In the compose deployment:

   ```bash
   export LITELLM_URL=https://litellm.corp.example
   export LITELLM_DATABASE_URL='postgres://agentbox_ro:…@litellm-db.corp.example/litellm?sslmode=require'  # read-only; per-user spend
   export REVIEWER_MODEL=small                  # the model the permission reviewer uses (it gets its own key, created at start-up)
   export AGENTBOX_KEY_TTL=15m                  # optional; also AGENTBOX_KEY_RENEW_BEFORE, _KEY_RPM, _KEY_TPM, _KEY_PARALLEL
   docker compose -p agentbox -f deploy/compose/docker-compose.yml -f deploy/compose/external-litellm.yml \
     --env-file deploy/compose/.secrets/compose.env up -d
   ```

   `external-litellm.yml` stops the bundled LiteLLM and mock model server from
   starting. `LITELLM_URL` is the one setting that names your proxy: the gatekeeper
   mints keys on it, and the front Envoy sends the approved requests to it (scheme,
   host, port and an optional path prefix such as `https://corp.example/litellm`; it
   sets the `Host` header to the host, so a proxy that routes by name works).

   A LiteLLM behind your own CA: mount the CA bundle into `gatekeeper` and `reviewer`
   and set `SSL_CERT_FILE` to it, and into `front-envoy` with `LITELLM_CA_FILE` set to
   the same file (without it the system CAs are used). `scripts/test-external-litellm.sh`
   contains a working override file for exactly this.
6. **Allow the models.** In the Agentbox console, Policy: for each group, list the
   model names each group may use. The **first** name is the default every
   harness starts on.

## 4. Check it: `scripts/preflight.sh`

```bash
scripts/preflight.sh --url https://litellm.corp.example \
    --master-key-file ./litellm_master_key \
    --models claude-sonnet,claude-haiku,deepseek-v3
# a LiteLLM behind your own CA:   CA_FILE=corp-ca.pem scripts/preflight.sh …
```

It does what the gatekeeper will do, with throw-away keys it deletes afterwards:

| Check | Fails when |
|---|---|
| LiteLLM reachable | the URL, DNS, firewall or TLS trust is wrong |
| master key accepted | the key is wrong, or the proxy has no database |
| models configured | a name your groups use is missing from `model_list` |
| short-lived key can be minted | LiteLLM ignores `duration`, or returns no expiry |
| a key only works for its models | LiteLLM lets a key call a model outside its list (needs two models) |
| chat completions / messages API / responses API / streaming | a harness would not work (a missing Responses API only breaks Codex: a warning) |
| expired keys are refused | a 3-second key still works after 5 seconds |
| deleted keys stop working at once | a deleted key still works (LiteLLM key cache) |
| telemetry callback installed | `abx_callback` is not an active callback |

It exits 1 when anything fails, so it can gate a deployment or an upgrade. The
same check runs against the test bed as E2E-20.

## 5. Day to day

**Add a model.** Add it to LiteLLM, run the check, then add the name to the groups
that may use it in the console. Devices get the new policy within seconds.

**Change the default or small model.** Reorder the group's list (first = default;
a name with `small`, `haiku`, `mini`, `flash`, `nano`, `lite` or `fast` as a whole
word is the small model). Sandboxes read their model names when they start:
users pick the change up with `agentbox recycle <sb>` (stop and start).

**Remove a model.** Remove it from the groups first (calls for it are refused with
`model_not_allowed` and a message that lists what is allowed), then from LiteLLM.

**Budgets and limits.** Per user: `budget_usd` in the console (the gateway
refuses with 402 `budget_exceeded` once LiteLLM's spend for that `user_id` reaches
it). Per key: `AGENTBOX_KEY_RPM`, `_TPM`, `_PARALLEL`. Per device: the policy's
`rate_limit_rpm` (enforced on the appliance). Emergency: the console's kill
switches pause everyone, a group, or one model at the gateway while LiteLLM keeps
running.

**Keys.** There is nothing to rotate by hand. To change the lifetime, set
`AGENTBOX_KEY_TTL` and restart the gatekeeper. Shorter is safer and costs one
extra `/key/generate` per sandbox per `TTL × ⅔`; 5 minutes is fine, below 2 minutes
is not worth it. Spend stays attributed to the user across key changes.

**The master key.** Change it in LiteLLM and in the gatekeeper's secret together
and restart both. Virtual keys keep working. Never change `LITELLM_SALT_KEY`: it
encrypts what LiteLLM stores.

**Upgrades.** Pin the image. Before you roll a new LiteLLM version out, run the
check against it; roll back if anything that was `ok` is not.

**Watch.** `gateway.denied` events in the console (by reason: `budget_exceeded`,
`model_not_allowed`, `attestation_failed`, `kill_switch`, `model_service_unavailable`);
`model.error` events from the callback; the gatekeeper's log line
`deleted N expired LiteLLM keys` (once a minute when sandboxes are active);
LiteLLM's own request and error rates.

## 6. Keeping the proxy private

The gatekeeper's keys are only as safe as the path to LiteLLM. Allow inbound
connections to LiteLLM from the gatekeeper host, your operators and your
monitoring only; never from user networks or the sandboxes' network. If the same
LiteLLM serves other teams, give them their own keys and models as before:
Agentbox's keys all have the alias prefix `abx:` and `metadata.agentbox.minted_by:
gatekeeper`, so they are easy to filter out of reports. Do not set team-level
model overrides or wildcard access that would widen what an `abx:` key may call:
the check "a key only works for its models" catches that.

## 7. Troubleshooting

| What a user or log shows | Cause and fix |
|---|---|
| `model service unreachable, try again shortly` | the gatekeeper cannot mint a key: LiteLLM down, wrong `LITELLM_URL`, TLS trust, or master key rejected. Run the check. |
| `model X is not in your policy (allowed: …)` | the harness asked for a name the group does not list. Add the name to the group, or fix the harness model setting. |
| Claude Code says the model does not exist or is not available | the name is not in LiteLLM's `model_list`, or not in the group. The sandbox sets all Claude model roles from the policy; a user who overrides `--model` must use an allowed name. |
| Claude Code prints "doesn't recognize the model … context window … 200k" | harmless for a 200k model. For a larger window set `CLAUDE_CODE_MAX_CONTEXT_TOKENS` in the user's environment. |
| Codex fails, others work | `/v1/responses` is not served for that model (the check warns). |
| `budget exhausted … $x of $y` | raise the user's budget in the console; spend comes from LiteLLM. |
| Spend and budgets never move | the model has no prices, or `LITELLM_DATABASE_URL` is wrong or empty. |
| Console has no `model.call` events | callback not installed, or `OTLP_ENDPOINT` unreachable from LiteLLM. |
| 401 from LiteLLM after a key was valid | it expired or was deleted (device revoked or attestation lost): the next request mints a new one unless the device is not trusted. |
| LiteLLM key table keeps growing | the gatekeeper is not running long enough to prune, or crashes: check its log. Delete `abx:*` keys that expired more than a day ago in the LiteLLM admin UI. |
