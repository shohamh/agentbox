# Agentbox console UI

React + TypeScript + Vite. Built by `npm ci && npm run build` into `dist/`, which the
`agentbox-console` Go server serves (`index.html`, `/assets/*`, SPA fallback). No CDN assets,
no runtime network access. API: `/api/*` (cookie session, 401 redirects to `/auth/login`);
live updates over one `EventSource` on `/api/events`.

## data-testid contract (used by the Playwright suite)

- Nav: `nav-overview nav-devices nav-users nav-sandboxes nav-requests nav-policy nav-activity nav-alerts nav-kill nav-audit nav-settings`; `whoami` (`<user> (<role>)`), `logout`; `error-banner`, `notice`
- Rows: `row-device-<device_id>`, `row-user-<username>`, `row-sandbox-<sandbox id>`, `row-request-<id>`, `row-alert-<id>`, `row-kill-<scope>` (e.g. `row-kill-model:deepseek-v3`, `row-kill-global`), `row-audit-<id>`, `row-group-<name>`, `row-event-<index>`
- Row buttons (inside the row): `btn-revoke btn-recycle btn-stop btn-approve btn-deny btn-confirm btn-rollback btn-rule btn-ack btn-close btn-escalate btn-assign btn-toggle btn-edit btn-save btn-suspend btn-details btn-pin btn-timeline`
- Modal: `reason-input expires-input confirm-submit modal-error` (device level picker: `level-input`)
- Overview: `stat-devices-enrolled stat-devices-online stat-sandboxes-live stat-requests-open stat-alerts-open stat-calls-per-min` (also `stat-devices-attested stat-devices-revoked stat-requests-flagged stat-kill-on`)
- Device row cells: `cell-attestation cell-bundle cell-online cell-level`
- Users: inputs `user-tier user-budget` (edit mode), cells `cell-spend cell-budget`
- Request row cells: `cell-status cell-class cell-review cell-reason`
- Alert row cells: `cell-detection cell-title cell-status`; kill row: `cell-state`; audit row: `cell-actor cell-action`
- Policy: `policy-group policy-json btn-preview diff-output btn-publish catalog-json btn-save-catalog`
- Activity: `filter-sandbox filter-user filter-device filter-kind filter-hours filter-q btn-search btn-export timeline`
- Audit: `audit-q btn-search btn-export`; Requests/Alerts: `filter-status`
- Settings: `exporter-otlphttp-enabled exporter-otlphttp-endpoint exporter-splunk-enabled exporter-splunk-endpoint exporter-splunk-token exporter-splunk-index btn-save-exporters btn-test-otlphttp btn-test-splunk exporter-result retention-events retention-metadata retention-bodies btn-save-retention reviewer-model reviewer-sample btn-save-reviewer sso-issuer`

Notes: Test-send saves the exporters first (the API tests the saved settings). Policy publish has no
reason modal. The global kill switch shows the two-admin message (HTTP 202 `pending`) in `notice`.
