import { useState } from 'react'
import { api, Row } from '../api'
import { ago, Badge, can, Dialog, Empty, JSONBlock, Page, ReasonModal, useApp, useLoad } from '../ui'

type Modal = { kind: 'revoke' | 'recycle' | 'level'; dev: Row } | null

export default function Devices() {
  const { me, setError } = useApp()
  const { data, reload } = useLoad<Row[]>(() => api('GET', '/api/devices'), ['devices'], [], 10000)
  const [modal, setModal] = useState<Modal>(null)
  const [open, setOpen] = useState<string | null>(null)
  const [detail, setDetail] = useState<Row | null>(null)
  const [level, setLevel] = useState('C')
  const admin = can(me?.role, 'admin')

  async function toggle(id: string) {
    if (open === id) return setOpen(null)
    setOpen(id)
    setDetail(null)
    try {
      setDetail(await api('GET', `/api/devices/${encodeURIComponent(id)}`))
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const rows = data ?? []
  return (
    <Page title="Devices">
      {rows.length === 0 ? (
        <Empty>No devices enrolled yet. Run agentbox enroll on a PC.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Device</th>
              <th>User</th>
              <th>OS</th>
              <th>Appliance</th>
              <th>Attestation</th>
              <th>Level</th>
              <th>Bundle</th>
              <th>Last contact</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((d) => {
              const id = String(d.device_id)
              const att = d.revoked ? 'revoked' : d.attestation_status
              return [
                <tr key={id} data-testid={`row-device-${id}`}>
                  <td className="mono">{id}</td>
                  <td>{d.username}</td>
                  <td>{d.os}</td>
                  <td className="mono">{d.appliance_version || '-'}</td>
                  <td data-testid="cell-attestation">
                    <Badge value={att} />
                  </td>
                  <td data-testid="cell-level">{d.assurance_level}</td>
                  <td data-testid="cell-bundle">
                    {d.bundle_version}
                    {d.published_bundle != null && d.published_bundle !== d.bundle_version ? <span className="muted"> → v{d.published_bundle}</span> : null}
                  </td>
                  <td data-testid="cell-online">
                    <Badge value={d.online ? 'online' : 'offline'} /> <span className="muted">{ago(d.last_seen)}</span>
                  </td>
                  <td className="actions">
                    <button data-testid="btn-details" onClick={() => toggle(id)}>
                      {open === id ? 'Hide' : 'Details'}
                    </button>
                    {admin && (
                      <>
                        <button data-testid="btn-revoke" className="danger" disabled={!!d.revoked} onClick={() => setModal({ kind: 'revoke', dev: d })}>
                          Revoke
                        </button>
                        <button data-testid="btn-recycle" disabled={!!d.revoked} onClick={() => setModal({ kind: 'recycle', dev: d })}>
                          Recycle
                        </button>
                        <button
                          data-testid="btn-edit"
                          onClick={() => {
                            setLevel(String(d.assurance_level || 'C'))
                            setModal({ kind: 'level', dev: d })
                          }}
                        >
                          Level
                        </button>
                      </>
                    )}
                  </td>
                </tr>,
                open === id && (
                  <tr key={id + '-d'} className="sub">
                    <td colSpan={9}>
                      {d.revoked ? (
                        <p>
                          Revoked by <b>{d.revoked_by}</b> {ago(d.revoked_at)}: {d.revoked_reason}
                        </p>
                      ) : null}
                      <strong>Attestation evidence</strong>
                      <JSONBlock value={d.attestation_detail} />
                      <strong>Current policy bundle</strong>
                      {detail ? <JSONBlock value={detail.bundle ?? 'no bundle yet'} /> : <p className="muted">loading…</p>}
                    </td>
                  </tr>
                ),
              ]
            })}
          </tbody>
        </table>
      )}
      {modal?.kind === 'revoke' && (
        <ReasonModal
          title={`Revoke ${modal.dev.device_id}`}
          danger
          confirmLabel="Revoke"
          hint="Model calls from this device stop within 30 seconds. Re-enrolling restores access after review."
          onClose={() => setModal(null)}
          onSubmit={async (reason) => {
            await api('POST', `/api/devices/${encodeURIComponent(modal.dev.device_id)}/revoke`, { reason })
            reload()
          }}
        />
      )}
      {modal?.kind === 'recycle' && (
        <ReasonModal
          title={`Recycle appliance on ${modal.dev.device_id}`}
          confirmLabel="Recycle"
          required={false}
          onClose={() => setModal(null)}
          onSubmit={async (reason) => {
            await api('POST', `/api/devices/${encodeURIComponent(modal.dev.device_id)}/recycle`, { reason })
          }}
        />
      )}
      {modal?.kind === 'level' && (
        <Dialog
          title={`Assurance level for ${modal.dev.device_id}`}
          onClose={() => setModal(null)}
          onConfirm={async () => {
            await api('POST', `/api/devices/${encodeURIComponent(modal.dev.device_id)}/level`, { level })
            reload()
          }}
        >
          <label>
            Level
            <select data-testid="level-input" value={level} onChange={(e) => setLevel(e.target.value)}>
              <option value="A">A (server-hosted)</option>
              <option value="B">B (strict mode)</option>
              <option value="C">C (default)</option>
            </select>
          </label>
        </Dialog>
      )}
    </Page>
  )
}
