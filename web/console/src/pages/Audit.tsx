import { useState } from 'react'
import { api, Row } from '../api'
import { Empty, fmt, JSONBlock, Page, useLoad } from '../ui'

export default function Audit() {
  const [q, setQ] = useState('')
  const [applied, setApplied] = useState('')
  const { data } = useLoad<Row[]>(() => api('GET', '/api/audit' + (applied ? `?q=${encodeURIComponent(applied)}` : '')), ['audit'], [applied], 15000)
  const [open, setOpen] = useState<number | null>(null)
  const rows = data ?? []
  return (
    <Page
      title="Audit log"
      actions={
        <a className="btn" data-testid="btn-export" href={'/api/audit?format=csv' + (applied ? `&q=${encodeURIComponent(applied)}` : '')} download="audit.csv">
          Export CSV
        </a>
      }
    >
      <div className="panel row">
        <label>
          Search
          <input data-testid="audit-q" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && setApplied(q)} placeholder="actor, action or target" />
        </label>
        <button data-testid="btn-search" className="primary" onClick={() => setApplied(q)}>
          Search
        </button>
      </div>
      {rows.length === 0 ? (
        <Empty>No audit entries match.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>Time</th>
              <th>Actor</th>
              <th>Action</th>
              <th>Target</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => [
              <tr key={a.id} data-testid={`row-audit-${a.id}`}>
                <td>{a.id}</td>
                <td className="muted" style={{ whiteSpace: 'nowrap' }}>
                  {fmt(a.at)}
                </td>
                <td data-testid="cell-actor">
                  {a.actor} <span className="muted">{a.role}</span>
                </td>
                <td className="mono" data-testid="cell-action">
                  {a.action}
                </td>
                <td className="mono wrap">{a.target}</td>
                <td className="actions">
                  <button data-testid="btn-details" onClick={() => setOpen(open === a.id ? null : a.id)}>
                    {open === a.id ? 'Hide' : 'Details'}
                  </button>
                </td>
              </tr>,
              open === a.id && (
                <tr key={a.id + 'd'} className="sub">
                  <td colSpan={6}>
                    <JSONBlock value={a.detail} />
                  </td>
                </tr>
              ),
            ])}
          </tbody>
        </table>
      )}
    </Page>
  )
}
