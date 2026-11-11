import { useEffect, useRef, useState } from 'react'
import { api, msg, Row } from '../api'
import { can, Empty, fmt, Page, pretty, ReasonModal, useApp, useLoad } from '../ui'

function renderDiff(diff: Row[]): string {
  if (!Array.isArray(diff) || diff.length === 0) return 'No enrolled devices in this group.'
  return diff
    .map((d) => `${d.device} (${d.user})\n` + (d.changes?.length ? d.changes.map((c: string) => '  ' + c).join('\n') : '  no changes'))
    .join('\n\n')
}

export default function Policy() {
  const { me, setError, setNotice } = useApp()
  const { data, reload } = useLoad<Row>(() => api('GET', '/api/policy'), ['devices'])
  const admin = can(me?.role, 'admin')
  const groups: Row[] = data?.groups ?? []
  const [group, setGroup] = useState('')
  const [text, setText] = useState('')
  const [diff, setDiff] = useState('')
  const [catText, setCatText] = useState('')
  const dirty = useRef(false)
  const catDirty = useRef(false)

  useEffect(() => {
    if (!data) return
    if (!group && groups.length) setGroup(groups[0].name)
    const g = groups.find((x) => x.name === group)
    if (g && !dirty.current) setText(pretty(g.policy))
    if (!catDirty.current) setCatText(pretty(data.catalog))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, group])

  function parse(): Row | null {
    try {
      return JSON.parse(text)
    } catch (e) {
      setError('Policy JSON is invalid: ' + msg(e))
      return null
    }
  }

  async function preview() {
    const policy = parse()
    if (!policy) return
    try {
      setDiff(renderDiff(await api('POST', '/api/policy/preview', { group, policy })))
    } catch (e) {
      setError(msg(e))
    }
  }

  const [publishing, setPublishing] = useState(false)

  function askPublish() {
    if (parse()) setPublishing(true)
  }

  async function publish(reason: string) {
    const policy = parse()
    if (!policy) return
    const res = await api<Row>('PUT', `/api/policy/groups/${encodeURIComponent(group)}`, { policy, reason })
    dirty.current = false
    setDiff(renderDiff(res.diff))
    setNotice(`Policy for group ${group} signed and published to ${res.diff?.length ?? 0} device(s).`)
    reload()
  }

  async function saveCatalog() {
    try {
      await api('PUT', '/api/policy/catalog', JSON.parse(catText))
      catDirty.current = false
      setNotice('Catalog saved; bundles are being re-signed.')
      reload()
    } catch (e) {
      setError('Catalog: ' + msg(e))
    }
  }

  return (
    <Page title="Policy">
      <h2>Group defaults</h2>
      {groups.length === 0 ? (
        <Empty>No groups defined.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Group</th>
              <th>Models</th>
              <th>Egress</th>
              <th>Images</th>
              <th>Tier cap</th>
              <th>Updated</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <tr key={g.name} data-testid={`row-group-${g.name}`}>
                <td>{g.name}</td>
                <td className="mono wrap">{(g.policy?.models ?? []).join(', ')}</td>
                <td className="mono wrap">{(g.policy?.egress ?? []).join(', ')}</td>
                <td className="mono wrap">{(g.policy?.images ?? []).join(', ')}</td>
                <td>{g.policy?.tier_cap}</td>
                <td className="muted">
                  {g.updated_by} {fmt(g.updated_at)}
                </td>
                <td className="actions">
                  <button
                    data-testid="btn-edit"
                    onClick={() => {
                      dirty.current = false
                      setGroup(g.name)
                      setText(pretty(g.policy))
                      setDiff('')
                    }}
                  >
                    Edit
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h2>Edit, preview, sign and publish</h2>
      <div className="panel">
        <div className="row" style={{ marginBottom: 8 }}>
          <label>
            Group
            <select
              data-testid="policy-group"
              value={group}
              onChange={(e) => {
                dirty.current = false
                setGroup(e.target.value)
                setDiff('')
                const g = groups.find((x) => x.name === e.target.value)
                if (g) setText(pretty(g.policy))
              }}
            >
              {groups.map((g) => (
                <option key={g.name} value={g.name}>
                  {g.name}
                </option>
              ))}
            </select>
          </label>
          <button data-testid="btn-preview" onClick={preview} disabled={!group}>
            Preview diff per device
          </button>
          {admin && (
            <button data-testid="btn-publish" className="primary" onClick={askPublish} disabled={!group}>
              Sign and publish
            </button>
          )}
        </div>
        <textarea
          data-testid="policy-json"
          className="code"
          spellCheck={false}
          value={text}
          readOnly={!admin}
          onChange={(e) => {
            dirty.current = true
            setText(e.target.value)
          }}
        />
        <h3 style={{ marginTop: 12 }}>Per-device diff</h3>
        <pre className="diff" data-testid="diff-output">
          {diff}
        </pre>
      </div>

      <h2>Catalogs</h2>
      <div className="panel">
        <p className="muted">Image catalog (with pinned digests), egress catalog (auto / flagged / never), project roots and model list.</p>
        <textarea
          data-testid="catalog-json"
          className="code"
          spellCheck={false}
          value={catText}
          readOnly={!admin}
          onChange={(e) => {
            catDirty.current = true
            setCatText(e.target.value)
          }}
        />
        {admin && (
          <div style={{ marginTop: 8 }}>
            <button data-testid="btn-save-catalog" className="primary" onClick={saveCatalog}>
              Save catalog
            </button>
          </div>
        )}
      </div>

      <h2>Automatic rules</h2>
      {(data?.auto_rules ?? []).length === 0 ? (
        <Empty>No automatic rules yet. Turn a request pattern into one from the Requests page.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Pattern</th>
              <th>Created by</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            {(data?.auto_rules as Row[]).map((r) => (
              <tr key={r.id} data-testid={`row-rule-${r.id}`}>
                <td className="mono">{r.pattern}</td>
                <td>{r.created_by}</td>
                <td className="muted">{fmt(r.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {publishing && (
        <ReasonModal
          title={`Sign and publish policy for ${group}`}
          confirmLabel="Sign and publish"
          hint="Every online device in this group receives a new signed bundle within a minute. The reason is written to the audit log."
          onClose={() => setPublishing(false)}
          onSubmit={publish}
        />
      )}
    </Page>
  )
}
