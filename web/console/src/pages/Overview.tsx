import { Link } from 'react-router-dom'
import { api, Row } from '../api'
import { Page, Sparkline, useLoad } from '../ui'

export default function Overview() {
  const { data } = useLoad<Row>(() => api('GET', '/api/overview'), ['devices', 'sandboxes', 'requests', 'alerts', 'kill'], [], 15000)
  const d = data ?? {}
  const cards: [string, string, unknown, string][] = [
    ['stat-devices-enrolled', 'Enrolled devices', d.devices_enrolled ?? '-', '/devices'],
    ['stat-devices-online', 'Online devices', d.devices_online ?? '-', '/devices'],
    ['stat-sandboxes-live', 'Live sandboxes', d.sandboxes_live ?? '-', '/sandboxes'],
    ['stat-requests-open', 'Open requests', d.requests_open ?? '-', '/requests'],
    ['stat-alerts-open', 'Open alerts', d.alerts_open ?? '-', '/alerts'],
    ['stat-calls-per-min', 'Model calls / min', d.model_calls_per_min !== undefined ? Number(d.model_calls_per_min).toFixed(1) : '-', '/activity?kinds=model'],
  ]
  const series: number[] = ((d.model_calls_series as Row[]) ?? []).map((r) => Number(r.n))
  const kills: string[] = d.kill_switches_on ?? []
  return (
    <Page title="Overview">
      <div className="cards">
        {cards.map(([id, label, v, to]) => (
          <Link key={id} to={to} className="card">
            <div className="n" data-testid={id}>
              {String(v)}
            </div>
            <div className="l">{label}</div>
          </Link>
        ))}
      </div>
      <div className="cards">
        <div className="card">
          <div className="l">Model calls, last 30 minutes</div>
          <Sparkline values={series} />
        </div>
        <Link to="/devices" className="card">
          <div className="n" data-testid="stat-devices-attested">
            {String(d.devices_attested ?? '-')}
          </div>
          <div className="l">Attested devices</div>
        </Link>
        <Link to="/devices" className="card">
          <div className="n" data-testid="stat-devices-revoked">
            {String(d.devices_revoked ?? '-')}
          </div>
          <div className="l">Revoked devices</div>
        </Link>
        <Link to="/requests" className="card">
          <div className="n" data-testid="stat-requests-flagged">
            {String(d.requests_flagged ?? '-')}
          </div>
          <div className="l">Flagged for review</div>
        </Link>
        <Link to="/kill" className="card">
          <div className="n" data-testid="stat-kill-on">
            {kills.length}
          </div>
          <div className="l">Kill switches on</div>
          <div className="muted mono">{kills.join(', ')}</div>
        </Link>
      </div>
    </Page>
  )
}
