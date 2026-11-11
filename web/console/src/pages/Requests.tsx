import { useState } from 'react'
import { api, Row } from '../api'
import { ago, Badge, can, Empty, Page, ReasonModal, useApp, useLoad } from '../ui'

type Act = 'approve' | 'deny' | 'confirm' | 'rollback' | 'rule'

export default function Requests() {
  const { me } = useApp()
  const [filter, setFilter] = useState('')
  const { data, reload } = useLoad<Row[]>(() => api('GET', '/api/requests' + (filter ? `?status=${filter}` : '')), ['requests'], [filter], 15000)
  const [modal, setModal] = useState<{ act: Act; row: Row } | null>(null)
  const approver = can(me?.role, 'approver')
  const admin = can(me?.role, 'admin')
  const rows = data ?? []

  return (
    <Page
      title="Permission requests"
      actions={
        <label className="inline">
          Status
          <select data-testid="filter-status" value={filter} onChange={(e) => setFilter(e.target.value)}>
            <option value="">all</option>
            <option value="pending">pending</option>
            <option value="active">active</option>
            <option value="approved">approved</option>
            <option value="denied">denied</option>
            <option value="rolled_back">rolled back</option>
            <option value="expired">expired</option>
          </select>
        </label>
      }
    >
      {rows.length === 0 ? (
        <Empty>No requests.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>User</th>
              <th>Permission</th>
              <th>Reason</th>
              <th>Class</th>
              <th>Status</th>
              <th>Reviewer</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const live = r.status === 'active' || r.status === 'approved'
              const reviewed = r.review_status && r.review_status !== 'none'
              return (
                <tr key={r.id} data-testid={`row-request-${r.id}`}>
                  <td>{r.id}</td>
                  <td>
                    {r.username}
                    <div className="muted mono">{r.device_id}</div>
                  </td>
                  <td className="mono wrap">{r.permission}</td>
                  <td className="wrap" data-testid="cell-reason">
                    {r.reason}
                  </td>
                  <td data-testid="cell-class">
                    <Badge value={r.class} />
                  </td>
                  <td data-testid="cell-status">
                    <Badge value={r.status} />
                    {r.decided_by ? (
                      <div className="muted wrap">
                        {r.decided_by} {ago(r.decided_at)}
                        {r.decision_reason ? `: ${r.decision_reason}` : ''}
                      </div>
                    ) : null}
                    {r.expires_at ? <div className="muted">expires {new Date(r.expires_at).toLocaleString()}</div> : null}
                  </td>
                  <td data-testid="cell-review" className="wrap">
                    {reviewed ? (
                      <>
                        <Badge value={r.review_status} />
                        {r.review_reason ? <div className="muted">{r.review_reason}</div> : null}
                        {r.review_model ? <div className="muted mono">{r.review_model}</div> : null}
                      </>
                    ) : (
                      <span className="muted">-</span>
                    )}
                  </td>
                  <td className="actions">
                    {approver && r.status === 'pending' && (
                      <>
                        <button data-testid="btn-approve" className="primary" onClick={() => setModal({ act: 'approve', row: r })}>
                          Approve
                        </button>
                        <button data-testid="btn-deny" className="danger" onClick={() => setModal({ act: 'deny', row: r })}>
                          Deny
                        </button>
                      </>
                    )}
                    {approver && live && reviewed && (
                      <>
                        <button data-testid="btn-confirm" onClick={() => setModal({ act: 'confirm', row: r })}>
                          Confirm
                        </button>
                        <button data-testid="btn-rollback" className="danger" onClick={() => setModal({ act: 'rollback', row: r })}>
                          Roll back
                        </button>
                      </>
                    )}
                    {approver && live && !reviewed && (
                      <button data-testid="btn-rollback" onClick={() => setModal({ act: 'rollback', row: r })}>
                        Roll back
                      </button>
                    )}
                    {admin && r.class !== 'never' && (
                      <button data-testid="btn-rule" title="Turn this permission into an automatic rule" onClick={() => setModal({ act: 'rule', row: r })}>
                        Make automatic
                      </button>
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
          title={
            { approve: 'Approve', deny: 'Deny', confirm: 'Confirm reviewer verdict for', rollback: 'Roll back', rule: 'Create automatic rule from' }[modal.act] +
            ` #${modal.row.id} (${modal.row.permission})`
          }
          danger={modal.act === 'deny' || modal.act === 'rollback'}
          confirmLabel={{ approve: 'Approve', deny: 'Deny', confirm: 'Confirm', rollback: 'Roll back', rule: 'Create rule' }[modal.act]}
          required={modal.act === 'approve' || modal.act === 'deny' || modal.act === 'rollback'}
          expires={modal.act === 'approve'}
          hint={modal.act === 'deny' ? 'The user sees this reason in their CLI.' : modal.act === 'rule' ? 'Future identical requests are applied automatically.' : undefined}
          onClose={() => setModal(null)}
          onSubmit={async (reason, expires_hours) => {
            const body: Row = { reason }
            if (expires_hours) body.expires_hours = expires_hours
            if (modal.act === 'rule') body.pattern = modal.row.permission
            await api('POST', `/api/requests/${modal.row.id}/${modal.act}`, body)
            reload()
          }}
        />
      )}
    </Page>
  )
}
