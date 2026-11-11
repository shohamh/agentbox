import React, { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { api, ApiError, msg } from './api'

export type Role = 'viewer' | 'approver' | 'admin'
const rank: Record<string, number> = { viewer: 1, approver: 2, admin: 3 }
export const can = (role: string | undefined, need: Role) => !!role && (rank[role] ?? 0) >= rank[need]

export interface Me {
  user: string
  email: string
  role: Role
}

interface Ctx {
  me: Me | null
  setError: (m: string | null) => void
  notice: string | null
  setNotice: (m: string | null) => void
  subscribe: (fn: (topic: string) => void) => () => void
}

export const AppCtx = createContext<Ctx>(null as unknown as Ctx)
export const useApp = () => useContext(AppCtx)

/** Loads data, and reloads it when a live change event arrives for one of `topics`. */
export function useLoad<T>(fn: () => Promise<T>, topics: string[] = [], deps: unknown[] = [], pollMs = 0) {
  const { subscribe, setError } = useApp()
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const fnRef = useRef(fn)
  fnRef.current = fn
  const reload = useCallback(async () => {
    try {
      setData(await fnRef.current())
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) setError(msg(e))
    } finally {
      setLoading(false)
    }
  }, [setError])
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => {
    reload()
  }, [reload, ...deps])
  const key = topics.join(',')
  useEffect(() => {
    let t: number | undefined
    const off = subscribe((topic) => {
      if (topics.includes(topic)) {
        window.clearTimeout(t)
        t = window.setTimeout(reload, 250)
      }
    })
    return () => {
      off()
      window.clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subscribe, reload, key])
  useEffect(() => {
    if (!pollMs) return
    const i = window.setInterval(reload, pollMs)
    return () => window.clearInterval(i)
  }, [reload, pollMs])
  return { data, loading, reload }
}

export function ErrorBanner({ error, onClose }: { error: string | null; onClose: () => void }) {
  if (!error) return null
  return (
    <div className="banner error" data-testid="error-banner" role="alert">
      <span>{error}</span>
      <button className="link" onClick={onClose}>
        dismiss
      </button>
    </div>
  )
}

const OK = ['pass', 'active', 'approved', 'consistent', 'online', 'running', 'closed', 'confirmed', 'ok', 'low', 'true']
const BAD = ['fail', 'revoked', 'denied', 'unusual', 'escalate', 'high', 'critical', 'failed', 'rolled_back', 'offline', 'expired', 'never']
const WARN = ['pending', 'flagged', 'medium', 'sampled', 'acknowledged', 'open', 'approval', 'stopped', 'escalated', 'creating']

export function tone(s: unknown): string {
  const v = String(s ?? '').toLowerCase()
  if (OK.includes(v)) return 'ok'
  if (BAD.includes(v)) return 'bad'
  if (WARN.includes(v)) return 'warn'
  return 'neutral'
}

export function Badge({ value, testid }: { value: unknown; testid?: string }) {
  const v = String(value ?? '')
  return (
    <span className={'badge ' + tone(v)} data-testid={testid}>
      {v || '-'}
    </span>
  )
}

export const fmt = (ts: unknown): string => {
  if (!ts) return '-'
  const d = new Date(String(ts))
  return isNaN(d.getTime()) ? String(ts) : d.toLocaleString()
}

export const ago = (ts: unknown): string => {
  if (!ts) return 'never'
  const d = new Date(String(ts)).getTime()
  if (isNaN(d)) return String(ts)
  const s = Math.max(0, Math.round((Date.now() - d) / 1000))
  if (s < 60) return s + 's ago'
  if (s < 3600) return Math.round(s / 60) + 'm ago'
  if (s < 86400) return Math.round(s / 3600) + 'h ago'
  return Math.round(s / 86400) + 'd ago'
}

export const uptime = (ts: unknown): string => {
  if (!ts) return '-'
  const d = new Date(String(ts)).getTime()
  if (isNaN(d) || d <= 0) return '-'
  const s = Math.max(0, Math.round((Date.now() - d) / 1000))
  if (s < 60) return s + 's'
  if (s < 3600) return Math.floor(s / 60) + 'm'
  if (s < 86400) return Math.floor(s / 3600) + 'h ' + Math.floor((s % 3600) / 60) + 'm'
  return Math.floor(s / 86400) + 'd'
}

export const pretty = (v: unknown) => JSON.stringify(v, null, 2)

export function Dialog(props: {
  title: string
  onClose: () => void
  onConfirm: () => Promise<unknown>
  children: React.ReactNode
  confirmLabel?: string
  danger?: boolean
}) {
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  async function go() {
    setBusy(true)
    setErr('')
    try {
      await props.onConfirm()
      props.onClose()
    } catch (e) {
      setErr(msg(e))
      setBusy(false)
    }
  }
  return (
    <div className="overlay">
      <div className="modal" role="dialog" aria-label={props.title}>
        <h3>{props.title}</h3>
        {props.children}
        {err && (
          <div className="banner error" data-testid="modal-error">
            {err}
          </div>
        )}
        <div className="modal-actions">
          <button onClick={props.onClose}>Cancel</button>
          <button data-testid="confirm-submit" className={props.danger ? 'danger' : 'primary'} disabled={busy} onClick={go}>
            {props.confirmLabel ?? 'Confirm'}
          </button>
        </div>
      </div>
    </div>
  )
}

/** A confirmation that asks for a reason (and optionally an expiry in hours). */
export function ReasonModal(props: {
  title: string
  onClose: () => void
  onSubmit: (reason: string, expiresHours?: number) => Promise<unknown>
  label?: string
  required?: boolean
  expires?: boolean
  confirmLabel?: string
  danger?: boolean
  hint?: string
}) {
  const [reason, setReason] = useState('')
  const [exp, setExp] = useState('')
  const label = props.label ?? 'Reason'
  const required = props.required ?? true
  return (
    <Dialog
      title={props.title}
      onClose={props.onClose}
      confirmLabel={props.confirmLabel}
      danger={props.danger}
      onConfirm={async () => {
        if (required && !reason.trim()) throw new Error(`${label} is required`)
        const h = exp.trim() ? parseFloat(exp) : undefined
        if (h !== undefined && (isNaN(h) || h <= 0)) throw new Error('Expiry must be a positive number of hours')
        await props.onSubmit(reason.trim(), h)
      }}
    >
      {props.hint && <p className="muted">{props.hint}</p>}
      <label>
        {label}
        {required ? ' (required)' : ''}
        <textarea data-testid="reason-input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} autoFocus />
      </label>
      {props.expires && (
        <label>
          Expires after (hours, optional)
          <input data-testid="expires-input" type="number" min="0" step="any" value={exp} onChange={(e) => setExp(e.target.value)} />
        </label>
      )}
    </Dialog>
  )
}

export function Page({ title, actions, children }: { title: string; actions?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section>
      <header className="page-head">
        <h1>{title}</h1>
        <div className="page-actions">{actions}</div>
      </header>
      {children}
    </section>
  )
}

export function Empty({ children }: { children: React.ReactNode }) {
  return <div className="empty">{children}</div>
}

export function Sparkline({ values }: { values: number[] }) {
  if (values.length < 2) return <div className="muted">not enough data yet</div>
  const w = 260
  const h = 44
  const max = Math.max(1, ...values)
  const pts = values.map((v, i) => `${(i / (values.length - 1)) * w},${h - 2 - (v / max) * (h - 6)}`).join(' ')
  return (
    <svg width={w} height={h} className="spark" aria-label="model calls per minute, last 30 minutes">
      <polyline points={pts} fill="none" stroke="#2563eb" strokeWidth="2" />
    </svg>
  )
}

export function JSONBlock({ value }: { value: unknown }) {
  return <pre className="json">{typeof value === 'string' ? value : pretty(value)}</pre>
}
