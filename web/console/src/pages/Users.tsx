import { useState } from 'react'
import { api, msg, Row } from '../api'
import { Badge, can, Empty, Page, useApp, useLoad } from '../ui'

export default function Users() {
  const { me, setError } = useApp()
  const { data, reload } = useLoad<Row[]>(() => api('GET', '/api/users'), ['users', 'requests'], [], 15000)
  const [editing, setEditing] = useState<string | null>(null)
  const [tier, setTier] = useState('')
  const [budget, setBudget] = useState('')
  const admin = can(me?.role, 'admin')

  async function save(name: string) {
    try {
      await api('POST', `/api/users/${encodeURIComponent(name)}`, { tier: parseInt(tier, 10), budget_usd: parseFloat(budget) })
      setEditing(null)
      reload()
    } catch (e) {
      setError(msg(e))
    }
  }
  async function suspend(u: Row) {
    try {
      await api('POST', `/api/users/${encodeURIComponent(u.username)}`, { suspended: !u.suspended })
      reload()
    } catch (e) {
      setError(msg(e))
    }
  }

  const rows = data ?? []
  return (
    <Page title="Users">
      {rows.length === 0 ? (
        <Empty>No users yet. Users appear when they enroll a device.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Group</th>
              <th>Tier</th>
              <th>Devices</th>
              <th>Spend / budget (USD)</th>
              <th>Status</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((u) => {
              const name = String(u.username)
              const edit = editing === name
              const spend = Number(u.spend_usd ?? 0)
              const bud = Number(u.budget_usd ?? 0)
              return (
                <tr key={name} data-testid={`row-user-${name}`}>
                  <td>
                    {name}
                    <div className="muted">{u.email}</div>
                  </td>
                  <td>{u.grp}</td>
                  <td>
                    {edit ? <input data-testid="user-tier" type="number" min="0" max="9" style={{ width: 70 }} value={tier} onChange={(e) => setTier(e.target.value)} /> : u.tier}
                  </td>
                  <td className="mono">{((u.devices as string[]) ?? []).join(', ') || '-'}</td>
                  <td>
                    <span data-testid="cell-spend">{spend.toFixed(4)}</span> /{' '}
                    {edit ? (
                      <input data-testid="user-budget" type="number" min="0" step="any" style={{ width: 110 }} value={budget} onChange={(e) => setBudget(e.target.value)} />
                    ) : (
                      <span data-testid="cell-budget">{bud}</span>
                    )}
                    {!edit && bud > 0 && spend >= bud ? <span className="badge bad" style={{ marginLeft: 6 }}>over budget</span> : null}
                  </td>
                  <td>
                    <Badge value={u.suspended ? 'suspended' : 'active'} />
                  </td>
                  <td className="actions">
                    {admin &&
                      (edit ? (
                        <>
                          <button data-testid="btn-save" className="primary" onClick={() => save(name)}>
                            Save
                          </button>
                          <button onClick={() => setEditing(null)}>Cancel</button>
                        </>
                      ) : (
                        <>
                          <button
                            data-testid="btn-edit"
                            onClick={() => {
                              setEditing(name)
                              setTier(String(u.tier))
                              setBudget(String(u.budget_usd))
                            }}
                          >
                            Edit
                          </button>
                          <button data-testid="btn-suspend" onClick={() => suspend(u)}>
                            {u.suspended ? 'Unsuspend' : 'Suspend'}
                          </button>
                        </>
                      ))}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </Page>
  )
}
