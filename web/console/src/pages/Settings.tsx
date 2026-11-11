import { useEffect, useState } from 'react'
import { api, msg, Row } from '../api'
import { Badge, can, Empty, fmt, Page, useApp, useLoad } from '../ui'

export default function Settings() {
  const { me, setError, setNotice } = useApp()
  const { data, reload } = useLoad<Row>(() => api('GET', '/api/settings'), [])
  const rel = useLoad<Row>(() => api('GET', '/api/releases'), [])
  const admin = can(me?.role, 'admin')

  const [otlpOn, setOtlpOn] = useState(false)
  const [otlpEp, setOtlpEp] = useState('')
  const [splOn, setSplOn] = useState(false)
  const [splEp, setSplEp] = useState('')
  const [splTok, setSplTok] = useState('')
  const [splIdx, setSplIdx] = useState('agentbox')
  const [result, setResult] = useState('')
  const [events, setEvents] = useState('30')
  const [meta, setMeta] = useState('365')
  const [bodies, setBodies] = useState('30')
  const [rvModel, setRvModel] = useState('')
  const [rvSample, setRvSample] = useState('0.05')

  useEffect(() => {
    if (!data) return
    const ex = data.exporters ?? {}
    setOtlpOn(!!ex.otlphttp?.enabled)
    setOtlpEp(ex.otlphttp?.endpoint ?? '')
    setSplOn(!!ex.splunk_hec?.enabled)
    setSplEp(ex.splunk_hec?.endpoint ?? '')
    setSplTok(ex.splunk_hec?.token ?? '')
    setSplIdx(ex.splunk_hec?.index || 'agentbox')
    const r = data.retention ?? {}
    setEvents(String(r.events_days ?? 30))
    setMeta(String(r.metadata_days ?? 365))
    setBodies(String(r.bodies_days ?? 30))
    const rv = data.reviewer ?? {}
    setRvModel(rv.model ?? '')
    setRvSample(String(rv.sample_rate ?? 0.05))
  }, [data])

  const put = async (key: string, value: unknown) => api('PUT', `/api/settings/${key}`, value)

  async function saveExporters() {
    await put('exporters', {
      otlphttp: { enabled: otlpOn, endpoint: otlpEp },
      splunk_hec: { enabled: splOn, endpoint: splEp, token: splTok, index: splIdx },
    })
  }

  async function wrap(fn: () => Promise<void>, ok: string) {
    try {
      await fn()
      setNotice(ok)
      reload()
    } catch (e) {
      setError(msg(e))
    }
  }

  async function test(name: 'otlphttp' | 'splunk_hec') {
    setResult('sending…')
    try {
      await saveExporters()
      const r = await api<Row>('POST', '/api/settings/exporters/test', { name })
      setResult(r.ok ? `${name}: delivered (${r.status})` : `${name}: failed - ${r.error ?? r.status}`)
    } catch (e) {
      setResult(`${name}: failed - ${msg(e)}`)
    }
  }

  const manifests: Row[] = rel.data?.manifests ?? []
  const revoked: string[] = rel.data?.revoked ?? []

  async function toggleRelease(v: string) {
    const next = revoked.includes(v) ? revoked.filter((x) => x !== v) : [...revoked, v]
    await wrap(async () => {
      await put('revoked_releases', next)
      await rel.reload()
    }, `Release ${v} ${next.includes(v) ? 'revoked: appliances on it lose model access until they update' : 'restored'}.`)
  }

  return (
    <Page title="Settings">
      <h2>Telemetry exporters</h2>
      <div className="panel">
        <p className="muted">
          Everything is OpenTelemetry. Records always go to ClickHouse; enabling an exporter sends the same records to another backend, by configuration alone.
        </p>
        <h3>OTLP/HTTP</h3>
        <div className="row">
          <label className="inline">
            <input data-testid="exporter-otlphttp-enabled" type="checkbox" checked={otlpOn} disabled={!admin} onChange={(e) => setOtlpOn(e.target.checked)} /> Enabled
          </label>
          <label style={{ flex: 1 }}>
            Endpoint
            <input data-testid="exporter-otlphttp-endpoint" value={otlpEp} disabled={!admin} onChange={(e) => setOtlpEp(e.target.value)} placeholder="http://collector.example:4318" style={{ width: '100%' }} />
          </label>
          {admin && (
            <button data-testid="btn-test-otlphttp" onClick={() => test('otlphttp')}>
              Test send
            </button>
          )}
        </div>
        <h3 style={{ marginTop: 16 }}>Splunk HEC</h3>
        <div className="row">
          <label className="inline">
            <input data-testid="exporter-splunk-enabled" type="checkbox" checked={splOn} disabled={!admin} onChange={(e) => setSplOn(e.target.checked)} /> Enabled
          </label>
          <label style={{ flex: 1 }}>
            Endpoint
            <input data-testid="exporter-splunk-endpoint" value={splEp} disabled={!admin} onChange={(e) => setSplEp(e.target.value)} placeholder="https://splunk.example:8088/services/collector" style={{ width: '100%' }} />
          </label>
          <label>
            Token
            <input data-testid="exporter-splunk-token" type="password" value={splTok} disabled={!admin} onChange={(e) => setSplTok(e.target.value)} />
          </label>
          <label>
            Index
            <input data-testid="exporter-splunk-index" value={splIdx} disabled={!admin} onChange={(e) => setSplIdx(e.target.value)} />
          </label>
          {admin && (
            <button data-testid="btn-test-splunk" onClick={() => test('splunk_hec')}>
              Test send
            </button>
          )}
        </div>
        {admin && (
          <div style={{ marginTop: 12 }}>
            <button data-testid="btn-save-exporters" className="primary" onClick={() => wrap(saveExporters, 'Exporters saved; the central collector reloads its pipelines.')}>
              Save exporters
            </button>
          </div>
        )}
        <p data-testid="exporter-result" className="mono">
          {result}
        </p>
      </div>

      <h2>Retention</h2>
      <div className="panel row">
        <label>
          Full events (days)
          <input data-testid="retention-events" type="number" min="1" value={events} disabled={!admin} onChange={(e) => setEvents(e.target.value)} />
        </label>
        <label>
          Metadata (days)
          <input data-testid="retention-metadata" type="number" min="1" value={meta} disabled={!admin} onChange={(e) => setMeta(e.target.value)} />
        </label>
        <label>
          Prompt and response bodies (days)
          <input data-testid="retention-bodies" type="number" min="1" value={bodies} disabled={!admin} onChange={(e) => setBodies(e.target.value)} />
        </label>
        {admin && (
          <button
            data-testid="btn-save-retention"
            className="primary"
            onClick={() => wrap(() => put('retention', { events_days: +events, metadata_days: +meta, bodies_days: +bodies }), 'Retention saved.')}
          >
            Save
          </button>
        )}
      </div>

      <h2>Reviewer model</h2>
      <div className="panel row">
        <label>
          Model (must be a different family from the agents' model)
          <input data-testid="reviewer-model" value={rvModel} disabled={!admin} onChange={(e) => setRvModel(e.target.value)} />
        </label>
        <label>
          Human sample rate for "consistent"
          <input data-testid="reviewer-sample" type="number" min="0" max="1" step="0.01" value={rvSample} disabled={!admin} onChange={(e) => setRvSample(e.target.value)} />
        </label>
        {admin && (
          <button
            data-testid="btn-save-reviewer"
            className="primary"
            onClick={() => wrap(() => put('reviewer', { ...(data?.reviewer ?? {}), model: rvModel, sample_rate: +rvSample }), 'Reviewer settings saved.')}
          >
            Save
          </button>
        )}
      </div>

      <h2>Single sign-on</h2>
      <div className="panel">
        <div>
          Issuer: <span className="mono" data-testid="sso-issuer">{data?.sso?.issuer}</span>
        </div>
        <div>
          Client: <span className="mono">{data?.sso?.client_id}</span>
        </div>
        <p className="muted">Roles come from Keycloak realm roles: viewer, approver, admin.</p>
      </div>

      <h2>Appliance releases</h2>
      {manifests.length === 0 ? (
        <Empty>No signed release manifests loaded.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Version</th>
              <th>Flavor</th>
              <th>PCR 11</th>
              <th>Created</th>
              <th>State</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {manifests.map((m) => (
              <tr key={m.version + m.flavor} data-testid={`row-release-${m.version}-${m.flavor}`}>
                <td>{m.version}</td>
                <td>{m.flavor}</td>
                <td className="mono">{String(m.pcr11).slice(0, 16)}…</td>
                <td className="muted">{fmt(m.created)}</td>
                <td>
                  <Badge value={revoked.includes(m.version) ? 'revoked' : m.flavor === 'prod' ? 'ok' : 'dev (never accepted)'} />
                </td>
                <td className="actions">
                  {admin && (
                    <button data-testid="btn-toggle" onClick={() => toggleRelease(m.version)}>
                      {revoked.includes(m.version) ? 'Restore' : 'Revoke release'}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Page>
  )
}
