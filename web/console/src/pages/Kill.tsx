import { useState } from 'react'
import { api, Row } from '../api'
import { ago, Badge, can, Page, ReasonModal, useApp, useLoad } from '../ui'

export default function Kill() {
  const { me, setNotice } = useApp()
  const { data, reload } = useLoad<Row[]>(() => api('GET', '/api/kill'), ['kill'], [], 15000)
  const pol = useLoad<Row>(() => api('GET', '/api/policy'), [])
  const [modal, setModal] = useState<{ scope: string; enable: boolean; row?: Row } | null>(null)
  const admin = can(me?.role, 'admin')

  // Switch rows: global, every model and every group, whether or not a row exists yet.
  const byScope: Record<string, Row> = {}
  ;(data ?? []).forEach((r) => (byScope[r.scope] = r))
  const scopes = new Set<string>(['global'])
  const cat: Row = pol.data?.catalog ?? {}
  ;(cat.models ?? []).forEach((m: string) => scopes.add('model:' + m))
  ;(pol.data?.groups ?? []).forEach((g: Row) => {
    ;(g.policy?.models ?? []).forEach((m: string) => scopes.add('model:' + m))
    scopes.add('group:' + g.name)
  })
  Object.keys(byScope).forEach((s) => scopes.add(s))
  const order = (s: string) => (s === 'global' ? 0 : s.startsWith('model:') ? 1 : 2)
  const list = [...scopes].sort((a, b) => order(a) - order(b) || a.localeCompare(b))

  return (
    <Page title="Kill switches">
      <p className="muted">
        A switch blocks model calls at the gateway for everyone it covers within 30 seconds. Sandboxes keep running; only model calls stop. Turning off all model access needs a
        second admin to confirm.
      </p>
      <table>
        <thead>
          <tr>
            <th>Scope</th>
            <th>State</th>
            <th>Reason</th>
            <th>Set by</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {list.map((scope) => {
            const r = byScope[scope] ?? {}
            const on = !!r.enabled
            return (
              <tr key={scope} data-testid={`row-kill-${scope}`}>
                <td className="mono">{scope}</td>
                <td data-testid="cell-state">
                  <Badge value={on ? 'fail' : 'ok'} testid="kill-state" />
                  <span style={{ marginLeft: 6 }}>{on ? 'ON (calls blocked)' : 'off'}</span>
                  {r.pending_by ? (
                    <div className="muted">
                      pending: {r.pending_by} asked {ago(r.pending_at)} ("{r.pending_reason}"); waiting for a second admin
                    </div>
                  ) : null}
                </td>
                <td className="wrap">{r.reason || '-'}</td>
                <td>{r.set_by ? `${r.set_by} ${ago(r.set_at)}` : '-'}</td>
                <td className="actions">
                  {admin && (
                    <button data-testid="btn-toggle" className={on ? '' : 'danger'} onClick={() => setModal({ scope, enable: !on, row: r })}>
                      {on ? 'Turn off' : scope === 'global' && r.pending_by ? 'Confirm / request on' : 'Turn on'}
                    </button>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {modal && (
        <ReasonModal
          title={`${modal.enable ? 'Turn ON' : 'Turn off'} kill switch ${modal.scope}`}
          danger={modal.enable}
          confirmLabel={modal.enable ? 'Turn on' : 'Turn off'}
          hint={modal.enable && modal.scope === 'global' ? 'Global off needs a second admin: your request is recorded, and a different admin must confirm it.' : undefined}
          onClose={() => setModal(null)}
          onSubmit={async (reason) => {
            const res = await api<Row>('POST', '/api/kill', { scope: modal.scope, enabled: modal.enable, reason })
            if (res.pending) setNotice(res.message ?? 'A second admin must confirm turning off all model access.')
            else setNotice(`Kill switch ${modal.scope} is now ${modal.enable ? 'ON' : 'off'}.`)
            reload()
          }}
        />
      )}
    </Page>
  )
}
