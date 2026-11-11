import { spawn, spawnSync } from 'child_process';
import { Browser, BrowserContext, Page, expect } from '@playwright/test';

export const PASSWORD = 'Passw0rd!';
export const PROJECT = process.env.COMPOSE_PROJECT ?? 'agentbox';
export const RUN = Date.now().toString(36);
export const BASE = process.env.CONSOLE_URL ?? 'http://console.agentbox.corp:18080';

// ---------- docker access ----------

export interface Res {
  out: string;
  code: number;
  ms: number;
}

export function run(cmd: string, args: string[], opts: { input?: string; timeout?: number } = {}): Res {
  const t0 = Date.now();
  const r = spawnSync(cmd, args, { input: opts.input, timeout: opts.timeout ?? 120_000, encoding: 'utf8', maxBuffer: 64 << 20 });
  const code = r.status ?? (r.signal ? 124 : -1);
  return { out: ((r.stdout ?? '') + (r.stderr ?? '')).trim(), code, ms: Date.now() - t0 };
}

const cids = new Map<string, string>();
export function cid(svc: string): string {
  const hit = cids.get(svc);
  if (hit) return hit;
  const r = run('docker', ['ps', '-aq', '--filter', `label=com.docker.compose.project=${PROJECT}`, '--filter', `label=com.docker.compose.service=${svc}`]);
  const id = r.out.split(/\s+/)[0];
  if (!id) throw new Error(`no container for service ${svc} (project ${PROJECT}); is the stack up?`);
  cids.set(svc, id);
  return id;
}

export function execIn(svc: string, argv: string[], opts: { input?: string; timeout?: number } = {}): Res {
  const a = ['exec'];
  if (opts.input !== undefined) a.push('-i');
  return run('docker', [...a, cid(svc), ...argv], opts);
}

export function dockerEnv(svc: string): Record<string, string> {
  const r = run('docker', ['inspect', '-f', '{{json .Config.Env}}', cid(svc)]);
  const out: Record<string, string> = {};
  for (const kv of JSON.parse(r.out) as string[]) {
    const i = kv.indexOf('=');
    out[kv.slice(0, i)] = kv.slice(i + 1);
  }
  return out;
}

export function psql(sql: string, which: 'main' | 'litellm' = 'main'): string[][] {
  const url = which === 'litellm' ? dockerEnv('gatekeeper').LITELLM_DATABASE_URL : dockerEnv('permsvc').DATABASE_URL;
  const r = execIn('postgres', ['psql', url, '-tAX', '-F', '\x1f', '-c', sql]);
  if (r.code !== 0) throw new Error(`psql failed: ${r.out}\n${sql}`);
  return r.out.split('\n').filter((l) => l.trim() !== '').map((l) => l.split('\x1f'));
}

// ---------- ClickHouse ----------

export async function chQuery(sql: string): Promise<Record<string, any>[]> {
  const e = dockerEnv('correlator');
  const headers: Record<string, string> = {};
  if (e.CLICKHOUSE_USER) headers.Authorization = 'Basic ' + Buffer.from(`${e.CLICKHOUSE_USER}:${e.CLICKHOUSE_PASSWORD ?? ''}`).toString('base64');
  const res = await fetch(`${e.CLICKHOUSE_URL.replace(/\/$/, '')}/?database=otel`, { method: 'POST', body: sql + ' FORMAT JSONEachRow', headers });
  const text = await res.text();
  if (!res.ok) throw new Error(`clickhouse ${res.status}: ${text}\n${sql}`);
  return text.split('\n').filter((l) => l.trim()).map((l) => JSON.parse(l));
}

export const EV = "coalesce(nullIf(LogAttributes['event.name'],''), Body)";
export const SB = "coalesce(nullIf(LogAttributes['agentbox.sandbox'],''), ResourceAttributes['agentbox.sandbox'])";
export const USER = "coalesce(nullIf(ResourceAttributes['agentbox.user'],''), LogAttributes['agentbox.user'])";

export const sqlTime = (d: Date) => `parseDateTime64BestEffort('${new Date(d.getTime() - 2000).toISOString()}', 9)`;

// ---------- polling ----------

export async function pollUntil<T>(ms: number, what: string, fn: () => Promise<T | false | undefined | null | ''> | T | false | undefined | null | '', every = 500): Promise<T> {
  const end = Date.now() + ms;
  let last: unknown = undefined;
  for (;;) {
    try {
      const v = await fn();
      if (v) return v as T;
      last = v;
    } catch (e) {
      last = e;
    }
    if (Date.now() > end) throw new Error(`timed out after ${ms} ms waiting for ${what} (last: ${String(last)})`);
    await new Promise((r) => setTimeout(r, every));
  }
}

// ---------- the PC and its CLI ----------

export const cli = (pc: string, args: string[], opts: { input?: string; timeout?: number } = {}) => execIn(pc, ['agentbox', ...args], opts);

export interface Bg {
  done: Promise<Res>;
}

/** Runs a CLI command in the background (for commands that wait, like `allow`). */
export function cliBg(pc: string, args: string[]): Bg {
  const t0 = Date.now();
  const p = spawn('docker', ['exec', cid(pc), 'agentbox', ...args]);
  let out = '';
  p.stdout.on('data', (d) => (out += d));
  p.stderr.on('data', (d) => (out += d));
  const done = new Promise<Res>((resolve) => p.on('close', (code) => resolve({ out: out.trim(), code: code ?? -1, ms: Date.now() - t0 })));
  return { done };
}

export function ensureEnrolled(pc: string, user: string): void {
  const rows = psql(`SELECT attestation_status, revoked FROM devices WHERE device_id='${pc}'`);
  if (rows.length === 0 || rows[0][0] !== 'pass' || rows[0][1] !== 'f') {
    const r = execIn(pc, ['agentbox', 'enroll', '--user', user, '--password-stdin'], { input: PASSWORD });
    if (r.code !== 0) throw new Error(`enroll ${pc}/${user}: ${r.out}`);
  }
}

export async function waitAttested(pc: string): Promise<void> {
  await pollUntil(120_000, `${pc} attested with a bundle`, () => {
    const s = cli(pc, ['status']).out;
    return s.includes('attestation pass') && s.includes('policy      bundle v');
  }, 1000);
}

export interface Sb {
  pc: string;
  name: string;
  id: string;
  state: string;
  note: string;
}

export function listSb(pc: string): Sb[] {
  const r = cli(pc, ['list']);
  return r.out.split('\n').slice(1).map((l) => l.trim().split(/\s+/)).filter((f) => f.length >= 4).map((f) => ({ pc, name: f[0], id: f[1], state: f[3], note: f.slice(4).join(' ') }));
}

const b64 = (s: string) => Buffer.from(s).toString('base64');
const PRELUDE = 'set -a; [ -r /etc/agentbox/env ] && . /etc/agentbox/env; set +a; cd /workspace 2>/dev/null\n';
const remote = (script: string) => `echo ${b64(PRELUDE + script + '\n')} | base64 -d > /tmp/t.$$.sh && sh /tmp/t.$$.sh </dev/null; rc=$?; rm -f /tmp/t.$$.sh; exit $rc`;

export const sbRun = (sb: { pc: string; name: string }, script: string, timeout = 120_000) =>
  cli(sb.pc, ['shell', sb.name, '--', remote(script)], { timeout });
export const sbRunBg = (sb: { pc: string; name: string }, script: string) => cliBg(sb.pc, ['shell', sb.name, '--', remote(script)]);

const CALC = 'def add(a, b):\\n    return a + b\\n\\n\\ndef mul(a, b):\\n    return a * b\\n';

export async function ensureSandbox(pc: string, name: string, image = 'codex'): Promise<Sb> {
  let sb = listSb(pc).find((s) => s.name === name);
  if (!sb) {
    const r = cli(pc, ['new', process.env.SAMPLE_REPO ?? 'git.agentbox.corp/samples/calc', '--name', name, '--image', image]);
    if (r.code !== 0) throw new Error(`agentbox new ${name}: ${r.out}`);
  } else if (sb.state !== 'running') {
    cli(pc, ['start', name]);
  }
  sb = await pollUntil(120_000, `sandbox ${name} running with ssh`, () => {
    const s = listSb(pc).find((x) => x.name === name);
    return s && s.state === 'running' && sbRun(s, 'true', 30_000).code === 0 ? s : false;
  }, 1000);
  sbRun(sb, `[ -f /workspace/calc.py ] || printf '${CALC}' > /workspace/calc.py`, 30_000);
  return sb;
}

export interface Mc {
  code: number;
  body: string;
  ms: number;
}

const PY = `python3 - <<'PY'
import json, os, time, urllib.request, urllib.error
n = int(os.environ.get("N", "1")); out = []
for _ in range(n):
    req = urllib.request.Request("http://llm.agentbox:4000/v1/chat/completions",
        data=json.dumps({"model": os.environ.get("MODEL", "deepseek-v3"), "messages": [{"role": "user", "content": "ping"}]}).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + os.environ["AGENTBOX_TOKEN"]})
    t = time.time()
    try:
        r = urllib.request.urlopen(req, timeout=40); code, body = r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        code, body = e.code, e.read().decode()
    except Exception as e:
        code, body = 0, str(e)
    out.append({"code": code, "body": body[:400], "ms": (time.time() - t) * 1000})
print("RESULT " + json.dumps(out))
PY`;

export function modelCalls(sb: { pc: string; name: string }, model: string, n = 1): Mc[] {
  const r = sbRun(sb, `export N=${n} MODEL=${model}\n${PY}`, 600_000);
  const i = r.out.lastIndexOf('RESULT ');
  if (i < 0) throw new Error(`model call script gave no result: ${r.out}`);
  return JSON.parse(r.out.slice(i + 7)) as Mc[];
}
export const modelCall = (sb: { pc: string; name: string }, model = 'deepseek-v3') => modelCalls(sb, model, 1)[0];

// ---------- console: login, API, modals ----------

export type Who = 'bob' | 'carol' | 'frank' | 'erin';
export const ROLE: Record<Who, string> = { bob: 'admin', erin: 'admin', carol: 'approver', frank: 'viewer' };

export interface Session {
  ctx: BrowserContext;
  page: Page;
  user: Who;
}

export async function login(browser: Browser, user: Who): Promise<Session> {
  const ctx = await browser.newContext({ baseURL: BASE });
  const page = await ctx.newPage();
  await page.goto('/auth/login');
  await page.locator('#username').fill(user);
  await page.locator('#password').fill(PASSWORD);
  await page.locator('#kc-login').click();
  await expect(page.getByTestId('whoami')).toContainText(`${user} (${ROLE[user]})`);
  return { ctx, page, user };
}

export async function api(s: Session, method: string, path: string, data?: unknown): Promise<{ status: number; json: any; text: string }> {
  const r = await s.page.request.fetch(path, { method, data: data as any, headers: data !== undefined ? { 'Content-Type': 'application/json' } : undefined });
  const text = await r.text();
  let json: any = null;
  try {
    json = JSON.parse(text);
  } catch {
    /* not JSON */
  }
  return { status: r.status(), json, text };
}

export async function go(s: Session, nav: string): Promise<void> {
  await s.page.getByTestId(`nav-${nav}`).click();
  await expect(s.page.getByTestId('error-banner')).toHaveCount(0);
}

/** Fills and submits the reason modal when one opens after a row button. */
export async function confirmModal(page: Page, reason: string, expiresHours?: string): Promise<void> {
  const submit = page.getByTestId('confirm-submit');
  await submit.waitFor({ state: 'visible', timeout: 5000 });
  const r = page.getByTestId('reason-input');
  if (await r.count()) await r.fill(reason);
  if (expiresHours !== undefined && (await page.getByTestId('expires-input').count())) await page.getByTestId('expires-input').fill(expiresHours);
  await submit.click();
  await expect(submit).toBeHidden({ timeout: 10_000 });
}

export const row = (page: Page, kind: string, key: string) => page.getByTestId(`row-${kind}-${key}`);

/** Rolls back earlier active grants of a permission so a test starts clean. */
export async function clearGrants(s: Session, permission: string): Promise<void> {
  const reqs = (await api(s, 'GET', '/api/requests')).json as any[];
  for (const r of reqs) {
    if (r.permission === permission && (r.status === 'active' || r.status === 'approved')) {
      await api(s, 'POST', `/api/requests/${r.id}/rollback`, { reason: 'test cleanup' });
    }
  }
  await pollUntil(30_000, `${permission} removed from the PC's policy`, () => {
    const host = permission.split(':')[1];
    return !cli('pc-1', ['status']).out.split('\n').some((l) => l.startsWith('egress') && l.includes(host));
  }, 1000);
}

export const egressOn = (pc: string, host: string) => cli(pc, ['status']).out.split('\n').some((l) => l.startsWith('egress') && l.includes(host));

export async function requestByReason(s: Session, reason: string, statuses?: string[]): Promise<any> {
  return pollUntil(30_000, `request with reason ${reason}`, async () => {
    const reqs = (await api(s, 'GET', '/api/requests')).json as any[];
    return reqs.find((r) => r.reason === reason && (!statuses || statuses.includes(r.status)));
  });
}

// ---------- recorded admin actions (checked by UI-10) ----------

export const actions: { actor: string; action: string; target?: string }[] = [];
export const record = (actor: string, action: string, target?: string) => actions.push({ actor, action, target });

// ---------- OTLP sink ----------

export async function sinkStats(): Promise<{ total: number; byEvent: Record<string, number>; recent?: any[] }> {
  const base = process.env.SINK_URL ?? 'http://sink.agentbox.corp:4318';
  const r = await fetch(`${base}/stats`);
  if (!r.ok) throw new Error(`sink /stats: ${r.status}`);
  const j: any = await r.json();
  // The sink reports {otlp:{records,by_event,last}, hec:{...}}; older flat shape also accepted.
  const o = j.otlp ?? j;
  return { total: Number(o.records ?? o.log_records ?? o.logs ?? 0), byEvent: o.by_event ?? {}, recent: o.last ?? o.recent };
}
