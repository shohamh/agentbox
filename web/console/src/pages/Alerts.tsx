import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, msg, Row } from '../api'
import { ago, Badge, can, Empty, JSONBlock, Page, ReasonModal, useApp, useLoad } from '../ui'

export default function Alerts() {
  const { me, setError } = useApp()
  const [filter, setFilter] = useState('')
  const { data, reload } = useLoad<Row[]>(() => api('GET', '/api/alerts' + (filter ? `?status=${filter}` : '')), ['alerts'], [filter], 15000)
  const [open, setOpen] = useState<number | null>(null)
  const [modal, setModal] = useState<{ act: 'assign' | 'revoke'; row: Row } | null>(null)
  const approver = can(me?.role, 'approver')
  const admin = can(me?.role, 'admin')

  async function act(id: number, action: string) {
    try {
      await api('POST', `/api/alerts/${id}/${action}`, {})
      reload()
    } catch (e) {
      setError(msg(e))
    }
  }

  const rows = data ?? []
  return (
    <Page
      title="Alerts"
      actions={
        <label className="inline">
          Status
          <select data-testid="filter-status" value={filter} onChange={(e) => setFilter(e.target.value)}>
            <option value="">all</option>
            <option value="open">open</option>
            <option value="acknowledged">acknowledged</option>
            <option value="escalated">escalated</option>
            <option value="closed">closed</option>
          </select>
        </label>
      }
    >
      {rows.length === 0 ? (
        <Empty>No alerts. Nothing has tripped a detection.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>Detection</th>
              <th>Severity</th>
              <th>Alert</th>
              <th>Where</th>
              <th>Status</th>
              <th>Raised</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => [
              <tr key={a.id} data-testid={`row-alert-${a.id}`}>
                <td>{a.id}</td>
                <td data-testid="cell-detection">
                  <b>{a.detection}</b>
                </td>
                <td>
                  <Badge value={a.severity} />
                </td>
                <td className="wrap" data-testid="cell-title">
                  {a.title}
                </td>
                <td className="mono wrap">
                  {a.username} {a.device_id}
                  {a.sandbox ? (
                    <div>
                      <Link to={`/activity?sandbox=${encodeURIComponent(a.sandbox)}&timeline=1`}>{a.sandbox}</Link>
                    </div>
                  ) : null}
                </td>
                <td data-testid="cell-status">
                  <Badge value={a.status} />
                  {a.assignee ? <div className="muted">→ {a.assignee}</div> : null}
                </td>
                <td className="muted">{ago(a.created_at)}</td>
                <td className="actions">
                  <button data-testid="btn-details" onClick={() => setOpen(open === a.id ? null : a.id)}>
                    {open === a.id ? 'Hide' : 'Evidence'}
                  </button>
                  {approver && a.status !== 'closed' && (
                    <>
                      <button data-testid="btn-ack" disabled={a.status === 'acknowledged'} onClick={() => act(a.id, 'acknowledge')}>
                        Acknowledge
                      </button>
                      <button data-testid="btn-assign" onClick={() => setModal({ act: 'assign', row: a })}>
                        Assign
                      </button>
                      <button data-testid="btn-escalate" disabled={a.status === 'escalated'} onClick={() => act(a.id, 'escalate')}>
                        Escalate
                      </button>
                      <button data-testid="btn-close" onClick={() => act(a.id, 'close')}>
                        Close
                      </button>
                    </>
                  )}
                  {admin && a.device_id && a.status !== 'closed' && (
                    <button data-testid="btn-revoke" className="danger" onClick={() => setModal({ act: 'revoke', row: a })}>
                      Revoke device
                    </button>
                  )}
                </td>
              </tr>,
              open === a.id && (
                <tr key={a.id + 'e'} className="sub">
                  <td colSpan={8}>
                    <JSONBlock value={a.evidence} />
                  </td>
                </tr>
              ),
            ])}
          </tbody>
        </table>
      )}
      {modal?.act === 'assign' && (
        <ReasonModal
          title={`Assign alert #${modal.row.id}`}
          label="Assignee"
          confirmLabel="Assign"
          onClose={() => setModal(null)}
          onSubmit={async (assignee) => {
            await api('POST', `/api/alerts/${modal.row.id}/assign`, { assignee })
            reload()
          }}
        />
      )}
      {modal?.act === 'revoke' && (
        <ReasonModal
          title={`Revoke ${modal.row.device_id} from alert #${modal.row.id}`}
          danger
          confirmLabel="Revoke device"
          hint="Revokes the device immediately and closes this alert."
          onClose={() => setModal(null)}
          onSubmit={async (reason) => {
            await api('POST', `/api/alerts/${modal.row.id}/revoke`, { reason })
            reload()
          }}
        />
      )}
    </Page>
  )
}
