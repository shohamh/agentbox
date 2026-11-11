import { test, expect, Browser } from '@playwright/test';
import {
  RUN, ROLE, Session, Who, actions, api, chQuery, clearGrants, cli, cliBg, confirmModal, egressOn, EV, SB, USER, ensureEnrolled, ensureSandbox, go,
  listSb, login, modelCall, modelCalls, pollUntil, psql, record, requestByReason, row, sbRun, sbRunBg, sinkStats, sqlTime, waitAttested,
} from './helpers';

const PC1 = 'pc-1';
const PC2 = 'pc-2';
const UNIT_TASK = 'Add a unit test for calc.mul and run the unit test suite';

test.beforeAll(async () => {
  ensureEnrolled(PC1, 'alice');
  await waitAttested(PC1);
});

async function as<T>(browser: Browser, who: Who, fn: (s: Session) => Promise<T>): Promise<T> {
  const s = await login(browser, who);
  try {
    return await fn(s);
  } finally {
    await s.ctx.close();
  }
}

// ---------- UI-00: smoke ----------

test('UI-00 every console page loads and the overview shows live counts', async ({ browser }) => {
  await as(browser, 'bob', async (s) => {
    for (const n of ['overview', 'devices', 'users', 'sandboxes', 'requests', 'policy', 'activity', 'alerts', 'kill', 'audit', 'settings']) await go(s, n);
    await go(s, 'overview');
    for (const id of ['stat-devices-enrolled', 'stat-devices-online', 'stat-sandboxes-live', 'stat-requests-open', 'stat-alerts-open', 'stat-calls-per-min']) {
      await expect(s.page.getByTestId(id)).toBeVisible();
    }
    const shown = async () => Number((await s.page.getByTestId('stat-devices-enrolled').innerText()).replace(/\D/g, ''));
    // the card renders 0 until the first fetch lands
    await expect.poll(shown, { timeout: 20_000 }).toBeGreaterThanOrEqual(1);
    const ov = (await api(s, 'GET', '/api/overview')).json;
    await expect.poll(shown, { timeout: 20_000 }).toBe(ov.devices_enrolled);
  });
});

// ---------- UI-01: automatic permission ----------

test('UI-01 agentbox allow for an automatic-list egress host applies within 10 s and shows as automatic', async ({ browser }) => {
  await as(browser, 'bob', async (s) => {
    const perm = 'egress:pypi.agentbox.corp';
    await clearGrants(s, perm);
    const reason = `ui01-${RUN}`;
    const t0 = Date.now();
    const r = cli(PC1, ['allow', perm, '--reason', reason]);
    expect(r.code, r.out).toBe(0);
    await pollUntil(10_000, 'the host in the sandbox policy', () => egressOn(PC1, 'pypi.agentbox.corp'), 250);
    const took = Date.now() - t0;
    console.log(`UI-01 applied after ${took} ms (limit 10000, includes CLI start-up)`);
    expect(took).toBeLessThan(10_000);

    const req = await requestByReason(s, reason);
    expect(req.class).toBe('instant');
    expect(req.status).toBe('active');
    await go(s, 'requests');
    const rw = row(s.page, 'request', String(req.id));
    await expect(rw).toBeVisible();
    await expect(rw.getByTestId('cell-class')).toContainText(/instant|automatic/i);
    await expect(rw.getByTestId('cell-status')).toContainText(/active|applied/i);

    const audit = (await api(s, 'GET', `/api/audit?q=${encodeURIComponent(perm)}`)).json as any[];
    const entry = audit.find((a) => a.action === 'permission.request' && JSON.stringify(a.detail).includes(reason));
    expect(entry, 'permission.request audit entry').toBeTruthy();
    expect(JSON.stringify(entry.detail)).toContain('instant');
    await go(s, 'audit');
    await expect(row(s.page, 'audit', String(entry.id))).toBeVisible();
  });
});

// ---------- UI-02: approval, then denial ----------

test('UI-02 an approved change reaches the sandbox within 10 s; a denial reaches the CLI with the reason', async ({ browser }) => {
  await as(browser, 'carol', async (s) => {
    const adm = await login(browser, 'bob');
    try {
      await clearGrants(adm, 'egress:example.org');
      // approval
      const reasonA = `ui02-approve-${RUN}`;
      const a = cliBg(PC1, ['allow', 'egress:example.org', '--reason', reasonA, '--wait', '120']);
      const reqA = await requestByReason(s, reasonA, ['pending']);
      expect(reqA.class).toBe('approval');
      await go(s, 'requests');
      const rw = row(s.page, 'request', String(reqA.id));
      await expect(rw.getByTestId('cell-status')).toContainText(/pending/i);
      await rw.getByTestId('btn-approve').click();
      const t0 = Date.now();
      await confirmModal(s.page, 'ok for this project');
      record('carol', 'request.approve', 'egress:example.org');
      await pollUntil(10_000, 'approved host in the sandbox policy', () => egressOn(PC1, 'example.org'), 250);
      const took = Date.now() - t0;
      console.log(`UI-02 approval reached the PC after ${took} ms (limit 10000)`);
      expect(took).toBeLessThan(10_000);
      const resA = await a.done;
      expect(resA.code, resA.out).toBe(0);
      expect(resA.out).toContain('Approved by carol');
      await expect(rw.getByTestId('cell-status')).toContainText(/approved/i);

      // denial
      const reasonB = `ui02-deny-${RUN}`;
      const b = cliBg(PC1, ['allow', 'egress:example.net', '--reason', reasonB, '--wait', '120']);
      const reqB = await requestByReason(s, reasonB, ['pending']);
      const rb = row(s.page, 'request', String(reqB.id));
      await expect(rb).toBeVisible();
      await rb.getByTestId('btn-deny').click();
      await confirmModal(s.page, 'not needed for this project');
      record('carol', 'request.deny', 'egress:example.net');
      const resB = await Promise.race([b.done, new Promise<never>((_, rej) => setTimeout(() => rej(new Error('denial did not reach the CLI in 30 s')), 30_000))]);
      expect(resB.code).not.toBe(0);
      expect(resB.out).toContain('denied by carol');
      expect(resB.out).toContain('not needed for this project');
      expect(egressOn(PC1, 'example.net')).toBe(false);
      await clearGrants(adm, 'egress:example.org');
    } finally {
      await adm.ctx.close();
    }
  });
});

// ---------- UI-03: allowed but flagged ----------

test('UI-03 a flagged change shows the reviewer verdict; the admin can confirm one and roll one back', async ({ browser }) => {
  await as(browser, 'bob', async (s) => {
    const mk = async (host: string, tag: string) => {
      await clearGrants(s, `egress:${host}`);
      const reason = `ui03-${tag}-${RUN} personal data upload (unusual)`;
      const r = cli(PC1, ['allow', `egress:${host}`, '--reason', reason]);
      expect(r.code, r.out).toBe(0);
      const req = await requestByReason(s, reason);
      expect(req.class).toBe('flagged');
      await pollUntil(10_000, `${host} applied at once`, () => egressOn(PC1, host), 250);
      await pollUntil(120_000, 'the reviewer verdict', async () => {
        const cur = ((await api(s, 'GET', '/api/requests')).json as any[]).find((x) => x.id === req.id);
        return cur && ['unusual', 'escalate'].includes(cur.review_status) ? cur : false;
      }, 1000);
      return req.id as number;
    };
    const idA = await mk('build1.lab.agentbox.corp', 'confirm');
    await go(s, 'requests');
    let rw = row(s.page, 'request', String(idA));
    await expect(rw.getByTestId('cell-review')).toContainText(/unusual|escalate/i);
    const alerts = (await api(s, 'GET', '/api/alerts')).json as any[];
    expect(alerts.some((a) => a.detection === 'D10' && JSON.stringify(a.evidence).includes('build1.lab'))).toBe(true);
    await rw.getByTestId('btn-confirm').click();
    await confirmModal(s.page, 'checked with the user');
    record('bob', 'request.confirm', 'egress:build1.lab.agentbox.corp');
    await expect(rw.getByTestId('cell-review')).toContainText(/confirmed/i);
    expect(egressOn(PC1, 'build1.lab.agentbox.corp')).toBe(true);

    const idB = await mk('build2.lab.agentbox.corp', 'rollback');
    rw = row(s.page, 'request', String(idB));
    await expect(rw).toBeVisible();
    await rw.getByTestId('btn-rollback').click();
    const t0 = Date.now();
    await confirmModal(s.page, 'not consistent with the user work');
    record('bob', 'request.rollback', 'egress:build2.lab.agentbox.corp');
    await pollUntil(15_000, 'the rolled-back host removed from the sandbox policy', () => !egressOn(PC1, 'build2.lab.agentbox.corp'), 250);
    console.log(`UI-03 rollback reached the PC after ${Date.now() - t0} ms`);
    await expect(rw.getByTestId('cell-status')).toContainText(/rolled/i);
    await clearGrants(s, 'egress:build1.lab.agentbox.corp');
  });
});

// ---------- UI-04: stop and recycle during a task ----------

test('UI-04 stop and recycle a sandbox from the console during a Codex task', async ({ browser }) => {
  const sb = await ensureSandbox(PC1, 'ui04', 'codex');
  const t0 = new Date();
  const bg = sbRunBg(sb, 'codex exec --skip-git-repo-check "RUN: sleep 90 && echo finished"');
  await pollUntil(90_000, 'the long tool call running in the sandbox', async () => {
    const r = await chQuery(`SELECT count() AS n FROM otel_logs WHERE ${EV}='process.exec' AND ${SB}='${sb.id}' AND Timestamp >= ${sqlTime(t0)} AND LogAttributes['process.command_line'] LIKE '%sleep 90%'`);
    return Number(r[0]?.n) > 0;
  }, 1000);
  await as(browser, 'bob', async (s) => {
    await go(s, 'sandboxes');
    const rw = row(s.page, 'sandbox', sb.id);
    await expect(rw).toBeVisible();
    await rw.getByTestId('btn-stop').click();
    const t1 = Date.now();
    await confirmModal(s.page, 'ui-04 stop');
    record('bob', 'sandbox.stop', `${PC1}/${sb.id}`);
    await pollUntil(15_000, 'the sandbox stopped', () => listSb(PC1).find((x) => x.name === 'ui04')?.state === 'stopped', 200);
    const took = Date.now() - t1;
    console.log(`UI-04 stop took ${took} ms (limit 5000 plus CLI polling overhead)`);
    expect(took).toBeLessThan(6500);
    const listed = cli(PC1, ['list']).out;
    expect(listed).toContain('stopped by bob');
    expect(listed).toContain('ui-04 stop');
    const res = await Promise.race([bg.done, new Promise<never>((_, rej) => setTimeout(() => rej(new Error('the task did not end after the stop')), 30_000))]);
    expect(res.code).not.toBe(0);

    // recycle a running sandbox
    cli(PC1, ['start', 'ui04']);
    await pollUntil(60_000, 'ui04 running again', () => listSb(PC1).find((x) => x.name === 'ui04')?.state === 'running', 500);
    const t2 = new Date();
    await go(s, 'sandboxes');
    await row(s.page, 'sandbox', sb.id).getByTestId('btn-recycle').click();
    await confirmModal(s.page, 'ui-04 recycle');
    record('bob', 'sandbox.recycle', `${PC1}/${sb.id}`);
    await pollUntil(60_000, 'the recycled sandbox running with a stop entry on its timeline', async () => {
      const running = listSb(PC1).find((x) => x.name === 'ui04')?.state === 'running';
      const ev = await chQuery(`SELECT count() AS n FROM otel_logs WHERE ${EV}='sandbox.stopped' AND ${SB}='${sb.id}' AND Timestamp >= ${sqlTime(t2)} AND LogAttributes['reason'] LIKE '%recycled by bob%'`);
      return running && Number(ev[0]?.n) > 0;
    }, 1000);
  });
});

// ---------- UI-05: revoke ----------

test('UI-05 revoking a device stops model calls within 30 s; re-enrolling restores access', async ({ browser }) => {
  const sb = await ensureSandbox(PC1, 'ui05', 'codex');
  expect(modelCall(sb).code).toBe(200);
  await as(browser, 'bob', async (s) => {
    await go(s, 'devices');
    const rw = row(s.page, 'device', PC1);
    await expect(rw.getByTestId('cell-attestation')).toContainText(/pass/i);
    await rw.getByTestId('btn-revoke').click();
    const t0 = Date.now();
    await confirmModal(s.page, 'ui-05 revoke test');
    record('bob', 'device.revoke', PC1);
    const failed = await pollUntil(30_000, 'model calls to fail after the revoke', () => {
      const c = modelCall(sb);
      return c.code !== 200 ? c : false;
    }, 500);
    const took = Date.now() - t0;
    console.log(`UI-05 model calls failed after ${took} ms (limit 30000): ${failed.code} ${failed.body}`);
    expect(took).toBeLessThan(30_000);
    expect(failed.body.toLowerCase()).toContain('revoked');
    await expect(rw.getByTestId('cell-attestation')).toContainText(/revoked/i);
    await go(s, 'devices');
    await expect(row(s.page, 'device', PC1).getByTestId('cell-attestation')).toContainText(/revoked/i);

    // re-enrolling restores access
    const r = cli(PC1, ['enroll', '--user', 'alice', '--password-stdin'], { input: 'Passw0rd!' });
    expect(r.code, r.out).toBe(0);
    await waitAttested(PC1);
    await pollUntil(150_000, 'model calls to work again after re-enrolling', () => modelCall(sb).code === 200, 2000);
    await expect(row(s.page, 'device', PC1).getByTestId('cell-attestation')).toContainText(/pass/i, { timeout: 30_000 });
  });
});

// ---------- UI-06: kill switches ----------

test('UI-06 a per-model kill switch blocks that model for everyone within 30 s; a global one needs a second admin', async ({ browser }) => {
  ensureEnrolled(PC2, 'dave');
  await waitAttested(PC2);
  const a = await ensureSandbox(PC1, 'ui06', 'codex');
  const d = await ensureSandbox(PC2, 'ui06', 'codex');
  for (const sb of [a, d]) {
    expect(modelCall(sb, 'glm-4').code).toBe(200);
    expect(modelCall(sb, 'deepseek-v3').code).toBe(200);
  }
  await as(browser, 'bob', async (s) => {
    await go(s, 'kill');
    const rw = row(s.page, 'kill', 'model:glm-4');
    await rw.getByTestId('btn-toggle').click();
    const t0 = Date.now();
    await confirmModal(s.page, 'ui-06 model off');
    record('bob', 'kill.on', 'model:glm-4');
    for (const sb of [a, d]) {
      const c = await pollUntil(30_000, `glm-4 to fail on ${sb.pc}`, () => {
        const x = modelCall(sb, 'glm-4');
        return x.code !== 200 ? x : false;
      }, 500);
      expect(c.code).toBe(503);
      expect(c.body).toContain('glm-4');
      expect(modelCall(sb, 'deepseek-v3').code, 'other models keep working').toBe(200);
    }
    console.log(`UI-06 glm-4 blocked for both users after ${Date.now() - t0} ms (limit 30000)`);
    expect(Date.now() - t0).toBeLessThan(60_000);
    await rw.getByTestId('btn-toggle').click();
    await confirmModal(s.page, 'ui-06 model back on');
    record('bob', 'kill.off', 'model:glm-4');
    for (const sb of [a, d]) await pollUntil(60_000, `glm-4 to work again on ${sb.pc}`, () => modelCall(sb, 'glm-4').code === 200, 1000);

    // the global switch: one admin only requests it, a different admin confirms
    const bob = s;
    const erin = await login(browser, 'erin');
    try {
      const first = await api(bob, 'POST', '/api/kill', { scope: 'global', enabled: true, reason: 'ui-06 global' });
      expect(first.status).toBe(202);
      const same = await api(bob, 'POST', '/api/kill', { scope: 'global', enabled: true, reason: 'ui-06 global again' });
      expect(same.status).toBe(409);
      expect(modelCall(a).code, 'still on before confirmation').toBe(200);
      try {
        const second = await api(erin, 'POST', '/api/kill', { scope: 'global', enabled: true, reason: 'ui-06 global confirm' });
        expect(second.status, second.text).toBe(200);
        record('erin', 'kill.on', 'global');
        await pollUntil(30_000, 'every model call to fail', () => modelCall(a).code !== 200);
        expect(modelCall(d, 'deepseek-v3').code).not.toBe(200);
      } finally {
        const off = await api(erin, 'POST', '/api/kill', { scope: 'global', enabled: false, reason: 'ui-06 done' });
        expect(off.status, off.text).toBe(200);
        record('erin', 'kill.off', 'global');
      }
      await pollUntil(60_000, 'model calls to resume', () => modelCall(a).code === 200, 1000);
    } finally {
      await erin.ctx.close();
    }
  });
});

// ---------- UI-07: budget ----------

test('UI-07 a budget below current use gives Codex and opencode a clear budget error, not a hang', async ({ browser }) => {
  const sb = await ensureSandbox(PC1, 'ui07', 'codex');
  const oc = await ensureSandbox(PC1, 'ui07-oc', 'opencode');
  await as(browser, 'bob', async (s) => {
    const users = (await api(s, 'GET', '/api/users')).json as any[];
    const alice = users.find((u) => u.username === 'alice');
    const original = String(alice.budget_usd);
    await pollUntil(60_000, 'a healthy model call before lowering the budget', () => modelCall(sb).code === 200, 1000);
    try {
      await go(s, 'users');
      const rw = row(s.page, 'user', 'alice');
      await rw.getByTestId('btn-edit').click();
      await rw.getByTestId('user-budget').fill('0');
      await rw.getByTestId('btn-save').click();
      if (await s.page.getByTestId('confirm-submit').isVisible().catch(() => false)) await confirmModal(s.page, 'ui-07 budget');
      record('bob', 'user.update', 'alice');
      await pollUntil(30_000, 'budget 0 saved', async () => ((await api(s, 'GET', '/api/users')).json as any[]).find((u) => u.username === 'alice').budget_usd === 0, 500);
      const c = await pollUntil(30_000, 'budget error on model calls', () => {
        const x = modelCall(sb);
        return (x.code === 402 || x.code === 429) ? x : false;
      }, 1000);
      expect(c.body.toLowerCase()).toContain('budget');
      for (const [name, script] of [
        ['codex', `timeout 120 codex exec --skip-git-repo-check "${UNIT_TASK}" 2>&1; echo "EXIT=$?"`, sb],
        ['opencode', `timeout 120 opencode run "${UNIT_TASK}" 2>&1; echo "EXIT=$?"`, oc],
      ] as [string, string, { pc: string; name: string }][]) {
        const r = sbRun(name === 'codex' ? sb : oc, script, 180_000);
        expect(r.out, `${name} must not hang`).not.toContain('EXIT=124');
        expect(r.out.toLowerCase(), `${name} output`).toContain('budget');
      }
    } finally {
      const reset = await api(s, 'POST', `/api/users/alice`, { budget_usd: Number(original) });
      expect(reset.status).toBe(200);
      record('bob', 'user.update', 'alice');
    }
    await pollUntil(30_000, 'model access back after restoring the budget', () => modelCall(sb).code === 200, 1000);
  });
});

// ---------- UI-08: group policy ----------

test('UI-08 editing a group default and publishing reaches every online appliance within 60 s', async ({ browser }) => {
  ensureEnrolled(PC2, 'dave');
  await waitAttested(PC2);
  await as(browser, 'bob', async (s) => {
    const devs0 = (await api(s, 'GET', '/api/devices')).json as any[];
    const before = new Map<string, number>(devs0.map((d) => [d.device_id, Number(d.published_bundle ?? 0)]));
    await go(s, 'policy');
    const edit = row(s.page, 'group', 'default').getByTestId('btn-edit');
    if (await edit.count()) await edit.click();
    const g = s.page.getByTestId('policy-group');
    const tag = await g.evaluate((e) => e.tagName);
    if (tag === 'SELECT') await g.selectOption('default');
    else if (tag === 'INPUT') await g.fill('default');
    const area = s.page.getByTestId('policy-json');
    const orig = JSON.parse(await area.inputValue());
    const next = { ...orig, rate_limit_rpm: (Number(orig.rate_limit_rpm) || 120) + 7 };
    await area.fill(JSON.stringify(next, null, 2));
    await s.page.getByTestId('btn-preview').click();
    await expect(s.page.getByTestId('diff-output')).toContainText(/rate limit/i);
    await expect(s.page.getByTestId('diff-output')).toContainText(PC1);
    await s.page.getByTestId('btn-publish').click();
    const t0 = Date.now();
    await confirmModal(s.page, 'ui-08 rate limit tweak');
    record('bob', 'policy.publish', 'group:default');
    try {
      await pollUntil(60_000, 'every online device on the new bundle', async () => {
        const devs = (await api(s, 'GET', '/api/devices')).json as any[];
        const online = devs.filter((d) => d.online && !d.revoked);
        return online.length > 0 && online.every((d) => Number(d.published_bundle) > (before.get(d.device_id) ?? 0) && Number(d.bundle_version) === Number(d.published_bundle));
      }, 1000);
      console.log(`UI-08 all online appliances on the new bundle after ${Date.now() - t0} ms (limit 60000)`);
      expect(Date.now() - t0).toBeLessThan(60_000);
      await go(s, 'devices');
      const pub = Number(((await api(s, 'GET', '/api/devices/' + PC1)).json as any).published_bundle);
      await expect(row(s.page, 'device', PC1).getByTestId('cell-bundle')).toContainText(String(pub));
    } finally {
      const back = await api(s, 'PUT', '/api/policy/groups/default', { policy: orig, reason: 'ui-08 restore' });
      expect(back.status).toBe(200);
      record('bob', 'policy.publish', 'group:default');
    }
  });
});

// ---------- UI-09: timeline against the agent runs ----------

const parseTs = (s: string) => {
  const m = s.match(/^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})(?:\.(\d+))?/);
  if (!m) return NaN;
  return Date.parse(`${m[1]}T${m[2]}.${(m[3] ?? '0').slice(0, 3).padEnd(3, '0')}Z`);
};

test('UI-09 every tool call has matching process events and model-call counts match the LiteLLM spend log', async ({ browser }) => {
  const t0 = new Date();
  const cx = await ensureSandbox(PC1, 'ui09-codex', 'codex');
  const oc = await ensureSandbox(PC1, 'ui09-opencode', 'opencode');
  const t1 = new Date();
  for (const [sb, cmd] of [[cx, `codex exec --skip-git-repo-check "${UNIT_TASK}"`], [oc, `opencode run "${UNIT_TASK}"`]] as const) {
    const r = sbRun(sb, `rm -f /workspace/test_agent_added.py; ${cmd}`, 300_000);
    expect(r.code, r.out).toBe(0);
  }
  await as(browser, 'bob', async (s) => {
    for (const sb of [cx, oc]) {
      let rows: any[] = [];
      await pollUntil(120_000, `timeline of ${sb.name}`, async () => {
        rows = ((await api(s, 'GET', `/api/activity?hours=1&limit=3000&sandbox=${sb.id}`)).json as any[]).filter((r) => parseTs(r.ts) >= t1.getTime() - 2000);
        const n = (e: string) => rows.filter((r) => r.event === e).length;
        return n('gen_ai.tool_call') >= 1 && n('process.exec') >= 1 && n('model.call') >= 3;
      }, 2000);
      rows.sort((a, b) => parseTs(a.ts) - parseTs(b.ts));
      const procs = rows.filter((r) => r.event === 'process.exec').map((r) => ({ t: parseTs(r.ts), used: false }));
      for (const tc of rows.filter((r) => r.event === 'gen_ai.tool_call')) {
        const t = parseTs(tc.ts);
        const hit = procs.find((p) => !p.used && p.t >= t - 1000 && p.t <= t + 60_000);
        expect(hit, `${sb.name}: tool call at ${tc.ts} has no matching process event`).toBeTruthy();
        hit!.used = true;
      }
      // the UI shows the same timeline
      await go(s, 'activity');
      await s.page.getByTestId('filter-sandbox').fill(sb.id);
      await s.page.getByTestId('btn-search').click();
      await expect(s.page.getByText(/gen_ai\.tool_call/).first()).toBeVisible();
    }
    const nCh = await pollUntil(120_000, 'model.call events and LiteLLM spend rows to agree', async () => {
      const ev = await chQuery(`SELECT count() AS n FROM otel_logs WHERE ${EV}='model.call' AND ${USER}='alice' AND Timestamp >= ${sqlTime(t1)}`);
      const n = Number(ev[0]?.n);
      const spend = Number(psql(`SELECT count(*) FROM "LiteLLM_SpendLogs" WHERE "user"='alice' AND "startTime" >= '${new Date(t1.getTime() - 2000).toISOString()}'`, 'litellm')[0][0]);
      return n > 0 && n === spend ? n : false;
    }, 2000);
    console.log(`UI-09 ${nCh} model calls in the timeline equal the LiteLLM spend log`);
    void t0;
  });
});

// ---------- UI-11: exporters ----------

test('UI-11 an otlphttp exporter delivers the same records without gaps; Splunk HEC when available', async ({ browser }) => {
  const sb = await ensureSandbox(PC1, 'ui11', 'codex');
  await as(browser, 'bob', async (s) => {
    const endpoint = process.env.SINK_URL ?? 'http://sink.agentbox.corp:4318';
    await go(s, 'settings');
    const pageOn = async (on: boolean) => {
      const box = s.page.getByTestId('exporter-otlphttp-enabled');
      if (on) await box.check();
      else await box.uncheck();
      await s.page.getByTestId('exporter-otlphttp-endpoint').fill(endpoint);
      await s.page.getByTestId('btn-save-exporters').click();
      if (await s.page.getByTestId('confirm-submit').isVisible().catch(() => false)) await confirmModal(s.page, 'ui-11 exporters');
      record('bob', 'settings.exporters', 'exporters');
    };
    try {
      await pageOn(true);
      await s.page.getByTestId('btn-test-otlphttp').click();
      await expect(s.page.getByTestId('exporter-result')).toContainText(/ok|200|success|true/i, { timeout: 30_000 });
      await pollUntil(60_000, 'the test record at the sink', async () => ((await sinkStats()).byEvent['agentbox.exporter_test'] ?? 0) >= 1, 1000);

      // wait until the central collector forwards live records
      await pollUntil(150_000, 'the pipeline to forward model calls to the sink', async () => {
        const b = (await sinkStats()).byEvent['model.call'] ?? 0;
        modelCall(sb);
        await new Promise((r) => setTimeout(r, 8000));
        return ((await sinkStats()).byEvent['model.call'] ?? 0) > b;
      }, 500);

      // no gaps: N further calls arrive exactly N times at both destinations
      const N = 5;
      const sinkBefore = (await sinkStats()).byEvent['model.call'] ?? 0;
      const t0 = new Date();
      const calls = modelCalls(sb, 'deepseek-v3', N);
      expect(calls.every((c) => c.code === 200)).toBe(true);
      await pollUntil(120_000, 'ClickHouse and the sink to agree on the new calls', async () => {
        const ch = Number((await chQuery(`SELECT count() AS n FROM otel_logs WHERE ${EV}='model.call' AND ${SB}='${sb.id}' AND Timestamp >= ${sqlTime(t0)}`))[0]?.n);
        const sink = ((await sinkStats()).byEvent['model.call'] ?? 0) - sinkBefore;
        return ch === N && sink === N ? true : false;
      }, 2000);
      const st = await sinkStats();
      if (Array.isArray(st.recent) && st.recent.length) {
        const rec = st.recent.find((r: any) => (r['event.name'] ?? r.event) === 'model.call');
        if (rec) {
          const keys = Object.keys(rec.attrs ?? rec).filter((k) => k.startsWith('agentbox.') || k.startsWith('gen_ai.'));
          expect(keys.length).toBeGreaterThan(0);
        }
      }
    } finally {
      await go(s, 'settings');
      await pageOn(false);
    }
    if (process.env.SPLUNK_HEC_URL) {
      await s.page.getByTestId('exporter-splunk-enabled').check();
      await s.page.getByTestId('exporter-splunk-endpoint').fill(process.env.SPLUNK_HEC_URL);
      await s.page.getByTestId('exporter-splunk-token').fill(process.env.SPLUNK_HEC_TOKEN ?? '');
      await s.page.getByTestId('exporter-splunk-index').fill(process.env.SPLUNK_INDEX ?? 'agentbox');
      await s.page.getByTestId('btn-save-exporters').click();
      if (await s.page.getByTestId('confirm-submit').isVisible().catch(() => false)) await confirmModal(s.page, 'ui-11 splunk');
      record('bob', 'settings.exporters', 'exporters');
      await s.page.getByTestId('btn-test-splunk').click();
      await expect(s.page.getByTestId('exporter-result')).toContainText(/ok|200|success|true/i, { timeout: 30_000 });
      await s.page.getByTestId('exporter-splunk-enabled').uncheck();
      await s.page.getByTestId('btn-save-exporters').click();
      if (await s.page.getByTestId('confirm-submit').isVisible().catch(() => false)) await confirmModal(s.page, 'ui-11 splunk off');
    } else {
      console.log('UI-11 Splunk HEC part skipped: set SPLUNK_HEC_URL (and SPLUNK_HEC_TOKEN) to a Splunk test instance');
    }
  });
});

// ---------- UI-12: roles ----------

test('UI-12 viewer and approver accounts can do only what their role allows', async ({ browser }) => {
  const reason = `ui12-${RUN}`;
  const r = cli(PC1, ['allow', 'egress:example.com', '--reason', reason, '--wait', '0']);
  expect(r.code, r.out).toBe(0);
  const bob = await login(browser, 'bob');
  try {
    const req = await requestByReason(bob, reason, ['pending']);
    const id = String(req.id);

    await as(browser, 'frank', async (s) => {
      expect((await api(s, 'GET', '/api/devices')).status, 'a viewer can read').toBe(200);
      await go(s, 'requests');
      const b = row(s.page, 'request', id).getByTestId('btn-approve');
      if (await b.count()) await expect(b).toBeDisabled();
      await go(s, 'policy');
      const pub = s.page.getByTestId('btn-publish');
      if (await pub.count()) await expect(pub.first()).toBeDisabled();
      for (const [method, path, body] of [
        ['POST', `/api/requests/${id}/approve`, { reason: 'viewer try' }],
        ['POST', '/api/kill', { scope: 'model:glm-4', enabled: true, reason: 'viewer try' }],
        ['PUT', '/api/policy/groups/default', { policy: {}, reason: 'viewer try' }],
        ['POST', `/api/devices/${PC1}/revoke`, { reason: 'viewer try' }],
        ['POST', '/api/users/alice', { budget_usd: 0 }],
      ] as [string, string, unknown][]) {
        const x = await api(s, method, path, body);
        expect(x.status, `${ROLE.frank} ${method} ${path}`).toBe(403);
      }
      record('frank', 'denied');
    });

    await as(browser, 'carol', async (s) => {
      await go(s, 'policy');
      const pub = s.page.getByTestId('btn-publish');
      if (await pub.count()) await expect(pub.first()).toBeDisabled();
      for (const [method, path, body] of [
        ['PUT', '/api/policy/groups/default', { policy: {}, reason: 'approver try' }],
        ['POST', `/api/devices/${PC1}/revoke`, { reason: 'approver try' }],
        ['POST', '/api/kill', { scope: 'model:glm-4', enabled: true, reason: 'approver try' }],
      ] as [string, string, unknown][]) {
        const x = await api(s, method, path, body);
        expect(x.status, `approver ${method} ${path}`).toBe(403);
      }
      record('carol', 'denied');
      const ok = await api(s, 'POST', `/api/requests/${id}/deny`, { reason: 'ui-12 cleanup' });
      expect(ok.status, 'an approver can decide requests').toBe(200);
      record('carol', 'request.deny', 'egress:example.com');
    });

    const audit = (await api(bob, 'GET', '/api/audit?q=denied')).json as any[];
    expect(audit.some((a) => a.actor === 'frank' && a.action === 'denied')).toBe(true);
    expect(audit.some((a) => a.actor === 'carol' && a.action === 'denied')).toBe(true);
  } finally {
    await bob.ctx.close();
  }
});

// ---------- UI-10: audit (last: it checks everything the other tests did) ----------

test('UI-10 every recorded action is in the audit log with the admin name and in ClickHouse as an OTel record', async ({ browser }) => {
  expect(actions.length, 'earlier tests recorded no actions; run the whole file for UI-10').toBeGreaterThan(0);
  await as(browser, 'bob', async (s) => {
    const audit = (await api(s, 'GET', '/api/audit')).json as any[];
    const seen = new Set<string>();
    for (const a of actions) {
      const key = `${a.actor}|${a.action}|${a.target ?? ''}`;
      if (seen.has(key)) continue;
      seen.add(key);
      const hit = audit.find((x) => x.actor === a.actor && x.action === a.action && (!a.target || String(x.target).includes(a.target.split('/').pop() as string)));
      expect(hit, `audit entry for ${key}`).toBeTruthy();
    }
    await go(s, 'audit');
    const newest = audit[0];
    await expect(row(s.page, 'audit', String(newest.id))).toBeVisible();

    const pairs = new Set(actions.map((a) => `${a.actor}|${a.action}`));
    for (const p of pairs) {
      const [actor, action] = p.split('|');
      await pollUntil(120_000, `OTel record audit.${action} by ${actor} in ClickHouse`, async () => {
        const r = await chQuery(`SELECT count() AS n FROM otel_logs WHERE ${EV}='audit.${action}' AND LogAttributes['audit.actor']='${actor}'`);
        return Number(r[0]?.n) > 0;
      }, 2000);
    }
    // audit entries carry the actor's role
    expect(audit.filter((x) => x.actor === 'carol' && x.action.startsWith('request.')).every((x) => x.role === 'approver')).toBe(true);
    expect(audit.filter((x) => x.actor === 'bob' && x.action.startsWith('device.')).every((x) => x.role === 'admin')).toBe(true);
  });
});
