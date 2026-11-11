import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { NavLink, Route, Routes, useLocation } from 'react-router-dom'
import { api, msg } from './api'
import { AppCtx, ErrorBanner, Me } from './ui'
import Overview from './pages/Overview'
import Devices from './pages/Devices'
import Users from './pages/Users'
import Sandboxes from './pages/Sandboxes'
import Requests from './pages/Requests'
import Policy from './pages/Policy'
import Activity from './pages/Activity'
import Alerts from './pages/Alerts'
import Kill from './pages/Kill'
import Audit from './pages/Audit'
import Settings from './pages/Settings'

const NAV: [string, string, string][] = [
  ['overview', '/', 'Overview'],
  ['devices', '/devices', 'Devices'],
  ['users', '/users', 'Users'],
  ['sandboxes', '/sandboxes', 'Sandboxes'],
  ['requests', '/requests', 'Requests'],
  ['policy', '/policy', 'Policy'],
  ['activity', '/activity', 'Activity'],
  ['alerts', '/alerts', 'Alerts'],
  ['kill', '/kill', 'Kill switches'],
  ['audit', '/audit', 'Audit'],
  ['settings', '/settings', 'Settings'],
]

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [live, setLive] = useState(false)
  const listeners = useRef(new Set<(t: string) => void>())
  const loc = useLocation()

  useEffect(() => {
    api<Me>('GET', '/api/me')
      .then(setMe)
      .catch((e) => setError(msg(e)))
  }, [])

  // One EventSource for the whole app; pages subscribe to topics.
  useEffect(() => {
    if (!me) return
    const es = new EventSource('/api/events')
    es.addEventListener('hello', () => setLive(true))
    es.addEventListener('change', (ev) => {
      try {
        const topic = JSON.parse((ev as MessageEvent).data).topic as string
        listeners.current.forEach((fn) => fn(topic))
      } catch {
        /* ignore malformed event */
      }
    })
    es.onerror = () => setLive(false)
    return () => es.close()
  }, [me])

  useEffect(() => {
    setError(null)
    setNotice(null)
  }, [loc.pathname])

  const subscribe = useCallback((fn: (t: string) => void) => {
    listeners.current.add(fn)
    return () => {
      listeners.current.delete(fn)
    }
  }, [])

  const ctx = useMemo(() => ({ me, setError, notice, setNotice, subscribe }), [me, notice, subscribe])

  return (
    <AppCtx.Provider value={ctx}>
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">
            <span className="logo">▣</span> Agentbox
          </div>
          <nav>
            {NAV.map(([id, to, label]) => (
              <NavLink key={id} to={to} end={to === '/'} data-testid={`nav-${id}`} className={({ isActive }) => (isActive ? 'active' : '')}>
                {label}
              </NavLink>
            ))}
          </nav>
          <div className="who">
            <div data-testid="whoami">{me ? `${me.user} (${me.role})` : '…'}</div>
            <div className={'live ' + (live ? 'on' : 'off')}>{live ? '● live' : '○ reconnecting'}</div>
            <a href="/auth/logout" data-testid="logout">
              Sign out
            </a>
          </div>
        </aside>
        <main>
          <ErrorBanner error={error} onClose={() => setError(null)} />
          {notice && (
            <div className="banner info" data-testid="notice">
              <span>{notice}</span>
              <button className="link" onClick={() => setNotice(null)}>
                dismiss
              </button>
            </div>
          )}
          {me && (
            <Routes>
              <Route path="/" element={<Overview />} />
              <Route path="/devices" element={<Devices />} />
              <Route path="/users" element={<Users />} />
              <Route path="/sandboxes" element={<Sandboxes />} />
              <Route path="/requests" element={<Requests />} />
              <Route path="/policy" element={<Policy />} />
              <Route path="/activity" element={<Activity />} />
              <Route path="/alerts" element={<Alerts />} />
              <Route path="/kill" element={<Kill />} />
              <Route path="/audit" element={<Audit />} />
              <Route path="/settings" element={<Settings />} />
              <Route path="*" element={<Overview />} />
            </Routes>
          )}
        </main>
      </div>
    </AppCtx.Provider>
  )
}
