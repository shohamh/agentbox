import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, msg, Row } from '../api'
import { Badge, can, Empty, fmt, JSONBlock, Page, useApp, useLoad } from '../ui'

const KINDS: Record<string, string> = {
  '': '',
  model: 'model.',
  tool: 'gen_ai.tool_call,tool.,codex.tool',
  process: 'process.',
  network: 'net.,egress.,dns.,preview.,token.',
  file: 'file.,sync.',
  lifecycle: 'sandbox.,tunnel.,appliance.,bundle.',
}

function category(ev: string): string {
  const e = ev.toLowerCase()
  if (e.startsWith('model.') || e.startsWith('gen_ai.chat') || e.startsWith('gen_ai.')) return e.includes('tool') ? 'tool' : 'model'
  if (e.startsWith('tool.') || e.startsWith('codex.tool')) return 'tool'
  if (e.startsWith('process.')) return 'process'
  if (/^(net|egress|dns|preview|token)\./.test(e)) return 'network'
  if (/^(file|sync)\./.test(e)) return 'file'
  if (/^(sandbox|tunnel|appliance|bundle|image)\./.test(e)) return 'lifecycle'
  return 'other'
}

function summary(r: Row): string {
  const a: Row = r.attrs ?? {}
  const skip = new Set(['event.name', 'agentbox.sandbox', 'agentbox.user', 'agentbox.device'])
  return Object.entries(a)
    .filter(([k, v]) => !skip.has(k) && v !== '')
    .slice(0, 6)
    .map(([k, v]) => `${k}=${String(v).slice(0, 80)}`)
    .join('  ')
}

export default function Activity() {
  const { me, setError } = useApp()
  const [sp, setSp] = useSearchParams()
  const [sandbox, setSandbox] = useState(sp.get('sandbox') ?? '')
  const [user, setUser] = useState(sp.get('user') ?? '')
  const [device, setDevice] = useState(sp.get('device') ?? '')
  const [q, setQ] = useState(sp.get('q') ?? '')
  const [kind, setKind] = useState(sp.get('kinds') && KINDS[sp.get('kinds')!] !== undefined ? sp.get('kinds')! : '')
  const [hours, setHours] = useState('24')
  const [timeline, setTimeline] = useState(sp.get('timeline') === '1')
  const [auto, setAuto] = useState(true)
  const [applied, setApplied] = useState(0)
  const [open, setOpen] = useState<number | null>(null)
  const [pinned, setPinned] = useState<Set<number>>(new Set())
  const approver = can(me?.role, 'approver')

  const qs = (extra = '') => {
    const p = new URLSearchParams()
    if (sandbox) p.set('sandbox', sandbox)
    if (user) p.set('user', user)
    if (device) p.set('device', device)
    if (q) p.set('q', q)
    if (KINDS[kind]) p.set('kinds', KINDS[kind])
    p.set('hours', hours)
    p.set('limit', '500')
    return '?' + p.toString() + extra
  }

  const { data, reload, loading } = useLoad<Row[]>(() => api('GET', '/api/activity' + qs()), [], [applied], auto ? 10000 : 0)
  let rows = data ?? []
  if (timeline) rows = [...rows].reverse()

  function search() {
    const p: Record<string, string> = {}
    if (sandbox) p.sandbox = sandbox
    if (user) p.user = user
    if (device) p.device = device
    if (q) p.q = q
    if (kind) p.kinds = kind
    if (timeline) p.timeline = '1'
    setSp(p, { replace: true })
    setApplied((n) => n + 1)
  }

  async function pin(i: number, r: Row) {
    try {
      await api('POST', '/api/activity/pin', { sandbox: r.sandbox, event: r, note: 'pinned from the console' })
      setPinned(new Set(pinned).add(i))
    } catch (e) {
      setError(msg(e))
    }
  }

  const evName = (r: Row) => String(r.event || r.body || '')

  return (
    <Page
      title="Activity"
      actions={
        <>
          <label className="inline">
            <input type="checkbox" checked={timeline} onChange={(e) => setTimeline(e.target.checked)} /> Timeline view
          </label>
          <label className="inline">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} /> Auto-refresh
          </label>
          <a className="btn" data-testid="btn-export" href={'/api/activity' + qs('&format=csv')} download="activity.csv">
            Export CSV
          </a>
        </>
      }
    >
      <div className="panel row">
        <label>
          Sandbox
          <input data-testid="filter-sandbox" value={sandbox} onChange={(e) => setSandbox(e.target.value)} placeholder="sb-1234abcd" />
        </label>
        <label>
          User
          <input data-testid="filter-user" value={user} onChange={(e) => setUser(e.target.value)} />
        </label>
        <label>
          Device
          <input data-testid="filter-device" value={device} onChange={(e) => setDevice(e.target.value)} />
        </label>
        <label>
          Kind
          <select data-testid="filter-kind" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="">all events</option>
            <option value="model">model calls</option>
            <option value="tool">tool calls</option>
            <option value="process">processes</option>
            <option value="network">network</option>
            <option value="file">file sync</option>
            <option value="lifecycle">lifecycle</option>
          </select>
        </label>
        <label>
          Range
          <select data-testid="filter-hours" value={hours} onChange={(e) => setHours(e.target.value)}>
            <option value="1">1 hour</option>
            <option value="6">6 hours</option>
            <option value="24">24 hours</option>
            <option value="168">7 days</option>
          </select>
        </label>
        <label>
          Text
          <input data-testid="filter-q" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && search()} placeholder="search events" />
        </label>
        <button data-testid="btn-search" className="primary" onClick={search}>
          Search
        </button>
      </div>

      {rows.length === 0 ? (
        <Empty>{loading ? 'Loading…' : 'No events match.'}</Empty>
      ) : timeline ? (
        <div className="panel timeline" data-testid="timeline">
          {rows.map((r, i) => {
            const e = evName(r)
            return (
              <div key={i} className={'ev cat-' + category(e)} data-testid={`row-event-${i}`}>
                <span className="muted">{fmt(r.ts)}</span> <b>{e}</b> <span className="muted">{r.user}</span>
                <div className="mono wrap" style={{ maxWidth: 'none' }}>
                  {summary(r)}
                </div>
              </div>
            )
          })}
        </div>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Time</th>
              <th>Event</th>
              <th>User</th>
              <th>Sandbox</th>
              <th>Details</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => [
              <tr key={i} data-testid={`row-event-${i}`}>
                <td className="muted" style={{ whiteSpace: 'nowrap' }}>
                  {fmt(r.ts)}
                </td>
                <td>
                  <Badge value={category(evName(r))} /> <span className="mono">{evName(r)}</span>
                </td>
                <td>{r.user}</td>
                <td className="mono">{r.sandbox}</td>
                <td className="mono wrap" style={{ maxWidth: 520 }}>
                  {summary(r)}
                </td>
                <td className="actions">
                  <button data-testid="btn-details" onClick={() => setOpen(open === i ? null : i)}>
                    {open === i ? 'Hide' : 'Details'}
                  </button>
                  {approver && (
                    <button data-testid="btn-pin" disabled={pinned.has(i)} onClick={() => pin(i, r)}>
                      {pinned.has(i) ? 'Pinned' : 'Pin'}
                    </button>
                  )}
                </td>
              </tr>,
              open === i && (
                <tr key={i + 'd'} className="sub">
                  <td colSpan={6}>
                    <JSONBlock value={r} />
                  </td>
                </tr>
              ),
            ])}
          </tbody>
        </table>
      )}
      <p className="muted">
        {rows.length} event(s). <button className="link" onClick={reload}>refresh</button>
      </p>
    </Page>
  )
}
