-- Agentbox control-plane state (Postgres). Events live in ClickHouse.

CREATE TABLE IF NOT EXISTS inventory (
  device_id     text PRIMARY KEY,
  assigned_user text NOT NULL,
  os            text NOT NULL DEFAULT 'linux',
  model         text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS users (
  username    text PRIMARY KEY,
  email       text NOT NULL DEFAULT '',
  grp         text NOT NULL DEFAULT 'default',
  tier        int  NOT NULL DEFAULT 1,
  budget_usd  numeric NOT NULL DEFAULT 50,
  suspended   boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS devices (
  device_id          text PRIMARY KEY,
  username           text NOT NULL REFERENCES users(username),
  os                 text NOT NULL DEFAULT '',
  hostname           text NOT NULL DEFAULT '',
  ak_public          bytea,
  ek_public          bytea,
  ssh_public         text NOT NULL DEFAULT '',
  enrolled_at        timestamptz NOT NULL DEFAULT now(),
  revoked            boolean NOT NULL DEFAULT false,
  revoked_reason     text NOT NULL DEFAULT '',
  revoked_by         text NOT NULL DEFAULT '',
  revoked_at         timestamptz,
  assurance_level    text NOT NULL DEFAULT 'C',
  attestation_status text NOT NULL DEFAULT 'pending',
  attestation_detail jsonb NOT NULL DEFAULT '{}',
  attested_at        timestamptz,
  appliance_version  text NOT NULL DEFAULT '',
  bundle_version     bigint NOT NULL DEFAULT 0,
  last_seen          timestamptz,
  status             jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS nonces (
  nonce      text PRIMARY KEY,
  device_id  text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS groups (
  name       text PRIMARY KEY,
  policy     jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL DEFAULT 'system'
);

CREATE TABLE IF NOT EXISTS settings (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL DEFAULT 'system'
);

CREATE TABLE IF NOT EXISTS requests (
  id              bigserial PRIMARY KEY,
  username        text NOT NULL,
  device_id       text NOT NULL,
  permission      text NOT NULL,
  reason          text NOT NULL DEFAULT '',
  class           text NOT NULL,
  status          text NOT NULL,
  decided_by      text NOT NULL DEFAULT '',
  decision_reason text NOT NULL DEFAULT '',
  expires_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  decided_at      timestamptz,
  review_status   text NOT NULL DEFAULT 'none',
  review_reason   text NOT NULL DEFAULT '',
  review_model    text NOT NULL DEFAULT '',
  reviewed_at     timestamptz
);
CREATE INDEX IF NOT EXISTS requests_user ON requests(username);

CREATE TABLE IF NOT EXISTS auto_rules (
  id         bigserial PRIMARY KEY,
  pattern    text NOT NULL UNIQUE,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS bundles (
  device_id    text PRIMARY KEY,
  version      bigint NOT NULL,
  content_hash text NOT NULL,
  signed       jsonb NOT NULL,
  expires_at   timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS commands (
  id         bigserial PRIMARY KEY,
  device_id  text NOT NULL,
  kind       text NOT NULL,
  args       jsonb NOT NULL DEFAULT '{}',
  created_by text NOT NULL,
  reason     text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  acked_at   timestamptz,
  result     text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS kill_switches (
  scope          text PRIMARY KEY,
  enabled        boolean NOT NULL DEFAULT false,
  reason         text NOT NULL DEFAULT '',
  set_by         text NOT NULL DEFAULT '',
  set_at         timestamptz,
  pending_by     text NOT NULL DEFAULT '',
  pending_reason text NOT NULL DEFAULT '',
  pending_at     timestamptz
);

CREATE TABLE IF NOT EXISTS alerts (
  id         bigserial PRIMARY KEY,
  detection  text NOT NULL,
  severity   text NOT NULL,
  device_id  text NOT NULL DEFAULT '',
  username   text NOT NULL DEFAULT '',
  sandbox    text NOT NULL DEFAULT '',
  title      text NOT NULL,
  evidence   jsonb NOT NULL DEFAULT '{}',
  status     text NOT NULL DEFAULT 'open',
  assignee   text NOT NULL DEFAULT '',
  dedup_key  text UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit (
  id      bigserial PRIMARY KEY,
  at      timestamptz NOT NULL DEFAULT now(),
  actor   text NOT NULL,
  role    text NOT NULL DEFAULT '',
  action  text NOT NULL,
  target  text NOT NULL DEFAULT '',
  detail  jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS evidence_pins (
  id        bigserial PRIMARY KEY,
  sandbox   text NOT NULL,
  event     jsonb NOT NULL,
  note      text NOT NULL DEFAULT '',
  pinned_by text NOT NULL,
  at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS correlator_state (
  name      text PRIMARY KEY,
  watermark timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS console_sessions (
  id         text PRIMARY KEY,
  username   text NOT NULL,
  email      text NOT NULL DEFAULT '',
  roles      jsonb NOT NULL DEFAULT '[]',
  id_token   text NOT NULL DEFAULT '',
  expires_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_states (
  state      text PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);
