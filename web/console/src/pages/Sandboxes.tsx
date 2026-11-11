import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, Row } from '../api'
import { Badge, can, Empty, Page, ReasonModal, uptime, useApp, useLoad } from '../ui'

export default function Sandboxes() {
  const { me } = useApp()
  const { data, reload } = useLoad<Row[]>(() => api('GET', '/api/sandboxes'), ['sandboxes', 'devices'], [], 10000)
  const [modal, setModal] = useState<{ action: 'stop' | 'recycle'; row: Row } | null>(null)
  const admin = can(me?.role, 'admin')
  const rows = data ?? []
  return (
    <Page title="Sandboxes">
      {rows.length === 0 ? (
        <Empty>No sandboxes reported yet. Create one with agentbox new.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Sandbox</th>
              <th>Device</th>
              <th>User</th>
              <th>Image</th>
              <th>Harness</th>
              <th>State</th>
              <th>Uptime</th>
              <th>Policy</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const s: Row = r.sandbox ?? {}
              const id = String(s.id)
              return (
                <tr key={r.device_id + id} data-testid={`row-sandbox-${id}`}>
                  <td>
                    <span className="mono">{id}</span>
                    <div className="muted">{s.name !== id ? s.name : ''}</div>
                  </td>
                  <td className="mono">{r.device_id}</td>
                  <td>{r.username}</td>
                  <td className="mono wrap" title={String(s.digest ?? '')}>
                    {s.image}
                  </td>
                  <td>{s.harness || '-'}</td>
                  <td>
                    <Badge value={s.state} testid="cell-state" />
                    {s.stop_reason ? <div className="muted wrap">{s.stop_reason}</div> : null}
                    {s.error ? <div className="muted wrap">{s.error}</div> : null}
                  </td>
                  <td>{s.state === 'running' ? uptime(s.started) : '-'}</td>
                  <td>v{s.policy_version}</td>
                  <td className="actions">
                    <Link className="btn" data-testid="btn-timeline" to={`/activity?sandbox=${encodeURIComponent(id)}&timeline=1`}>
                      Timeline
                    </Link>
                    {admin && (
                      <>
                        <button data-testid="btn-stop" className="danger" disabled={s.state !== 'running'} onClick={() => setModal({ action: 'stop', row: r })}>
                          Stop
                        </button>
                        <button data-testid="btn-recycle" disabled={s.state !== 'running'} onClick={() => setModal({ action: 'recycle', row: r })}>
                          Recycle
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      {modal && (
        <ReasonModal
          title={`${modal.action === 'stop' ? 'Stop' : 'Recycle'} sandbox ${modal.row.sandbox.id}`}
          danger={modal.action === 'stop'}
          confirmLabel={modal.action === 'stop' ? 'Stop' : 'Recycle'}
          hint="The user sees who did this and why in their CLI."
          onClose={() => setModal(null)}
          onSubmit={async (reason) => {
            await api('POST', `/api/sandboxes/${encodeURIComponent(modal.row.device_id)}/${encodeURIComponent(modal.row.sandbox.id)}/${modal.action}`, { reason })
            reload()
          }}
        />
      )}
    </Page>
  )
}
