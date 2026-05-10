// CreateSessionDialog.js -- Modal form for creating a new session.
// Two modes: Arnold (default) — sandboxed Arnold session with just a path,
// Custom — full form with title, path, and tool selector.
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { createSessionDialogSignal, mutationsEnabledSignal } from './state.js'
import { Icon, ICONS } from './icons.js'
import { apiFetch } from './api.js'

const TOOLS = ['claude', 'codex', 'gemini', 'shell']

export function CreateSessionDialog() {
  const [mode, setMode] = useState('arnold')
  const [title, setTitle] = useState('')
  const [tool, setTool] = useState('claude')
  const [path, setPath] = useState('')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)

  if (!mutationsEnabledSignal.value) return null

  async function handleSubmit(e) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      if (mode === 'arnold') {
        await apiFetch('POST', '/api/sessions/quick-arnold', { projectPath: path })
      } else {
        await apiFetch('POST', '/api/sessions', { title, tool, projectPath: path })
      }
      createSessionDialogSignal.value = false
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const close = () => (createSessionDialogSignal.value = false)
  const handleBackdropClick = (e) => { if (e.target === e.currentTarget) close() }

  const isArnold = mode === 'arnold'

  return html`
    <div class="overlay" onClick=${handleBackdropClick}>
      <form class="dialog" onClick=${e => e.stopPropagation()} onSubmit=${handleSubmit}>
        <div class="dh">
          <span class="kicker">NEW</span>
          <div class="t">${isArnold ? 'New Arnold session' : 'New session'}</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label="Close">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          <div class="field">
            <label>MODE</label>
            <div class="seg-row">
              <button type="button" class=${`seg-btn ${isArnold ? 'on' : ''}`}
                      onClick=${() => setMode('arnold')}>Arnold</button>
              <button type="button" class=${`seg-btn ${!isArnold ? 'on' : ''}`}
                      onClick=${() => setMode('custom')}>Custom</button>
            </div>
          </div>
          ${!isArnold && html`
            <div class="field">
              <label>TITLE</label>
              <input required value=${title} onInput=${e => setTitle(e.target.value)} placeholder="my-session"/>
            </div>
          `}
          <div class="field">
            <label>WORKING DIR</label>
            <input autofocus required value=${path} onInput=${e => setPath(e.target.value)} placeholder="/absolute/path/to/project"/>
          </div>
          ${!isArnold && html`
            <div class="field">
              <label>TOOL</label>
              <div class="seg-row">
                ${TOOLS.map(t => html`
                  <button type="button" key=${t}
                          class=${`seg-btn ${tool === t ? 'on' : ''}`}
                          onClick=${() => setTool(t)}>${t}</button>
                `)}
              </div>
            </div>
          `}
          ${error && html`
            <div style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                        border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
              ${error}
            </div>
          `}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${close}>Cancel</button>
          <button type="submit" class="btn primary" disabled=${submitting || !path || (!isArnold && !title)}>
            ${submitting ? 'Creating…' : isArnold
              ? html`Launch Arnold <span class="kbd">⏎</span>`
              : html`Create session <span class="kbd">⏎</span>`}
          </button>
        </div>
      </form>
    </div>
  `
}
