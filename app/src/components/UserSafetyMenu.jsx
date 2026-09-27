import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import Modal from './Modal'
import {
  BLOCK_CONFIRM_BODY,
  FALLBACK_REPORT_REASONS,
  REPORT_SENT_BODY,
  blockConfirmTitle,
  safetyTargetName,
} from '../lib/safety'

/**
 * Report / Block for the other party on a task (App Store Guideline 1.2 —
 * web parity with mobile's UserSafetyMenu). A "⋯" button that opens a small
 * menu; Report opens a dialog of server-owned preset reasons (GET
 * /safety/report-reasons, the same closed set cancel reasons follow), Block
 * confirms and posts. The server checks that `userId` is really the other
 * party on `taskId` — this component only decides what to draw.
 */
export default function UserSafetyMenu({ userId, userName, taskId, blocked = false, onBlocked }) {
  const [menuOpen, setMenuOpen] = useState(false)
  const [mode, setMode] = useState(null) // 'report' | 'block' | null
  const [reasons, setReasons] = useState(FALLBACK_REPORT_REASONS)
  const [reasonCode, setReasonCode] = useState('')
  const [details, setDetails] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [done, setDone] = useState('')
  const [error, setError] = useState('')
  const menuRef = useRef(null)
  const name = safetyTargetName(userName)

  useEffect(() => {
    if (!menuOpen) return
    function onDoc(e) {
      if (menuRef.current && !menuRef.current.contains(e.target)) setMenuOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [menuOpen])

  useEffect(() => {
    if (mode !== 'report') return
    let live = true
    api('/safety/report-reasons')
      .then((r) => {
        if (live && r?.reasons?.length) setReasons(r.reasons)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [mode])

  function close() {
    if (submitting) return
    setMode(null)
    setReasonCode('')
    setDetails('')
    setDone('')
    setError('')
  }

  async function submitReport() {
    if (!reasonCode) return
    setSubmitting(true)
    setError('')
    try {
      await api('/safety/report', {
        method: 'POST',
        body: { user_id: userId, task_id: taskId, reason_code: reasonCode, details: details.trim() || undefined },
      })
      setDone(REPORT_SENT_BODY)
    } catch (e) {
      setError(e?.body?.message || e?.message || "Couldn't send your report. Try again.")
    } finally {
      setSubmitting(false)
    }
  }

  async function submitBlock() {
    setSubmitting(true)
    setError('')
    try {
      const res = await api('/safety/block', { method: 'POST', body: { user_id: userId, task_id: taskId } })
      setDone(res?.message || 'Blocked.')
      onBlocked?.()
    } catch (e) {
      setError(e?.body?.message || e?.message || "Couldn't block. Try again in a moment.")
    } finally {
      setSubmitting(false)
    }
  }

  const btn = 'px-3 py-1.5 rounded-md border border-white/20 hover:border-white/40 text-accent'
  const danger = 'px-3 py-1.5 rounded-md bg-red-500/90 hover:bg-red-500 text-black disabled:opacity-60'

  return (
    <div className="relative" ref={menuRef}>
      <button
        type="button"
        aria-label={`Report or block ${name}`}
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        onClick={() => setMenuOpen((o) => !o)}
        className="h-9 w-9 grid place-items-center rounded-full border border-white/15 hover:border-white/40 text-white/80"
      >
        ⋯
      </button>
      {menuOpen && (
        <div role="menu" className="absolute right-0 z-40 mt-2 w-48 rounded-lg border border-white/15 bg-[#1a1a2e] p-1 shadow-xl">
          <button
            role="menuitem"
            className="w-full rounded px-3 py-2 text-left text-sm hover:bg-white/10"
            onClick={() => {
              setMenuOpen(false)
              setMode('report')
            }}
          >
            Report {name}
          </button>
          {!blocked && (
            <button
              role="menuitem"
              className="w-full rounded px-3 py-2 text-left text-sm text-red-400 hover:bg-white/10"
              onClick={() => {
                setMenuOpen(false)
                setMode('block')
              }}
            >
              Block {name}
            </button>
          )}
        </div>
      )}

      <Modal
        open={mode === 'report'}
        onClose={close}
        title={done ? 'Report sent' : `Report ${name}`}
        actions={
          done ? (
            <button className="px-3 py-1.5 rounded-md bg-white/90 text-black hover:bg-white" onClick={close}>
              Done
            </button>
          ) : (
            <>
              <button className={btn} onClick={close} disabled={submitting}>
                Cancel
              </button>
              <button className={danger} onClick={submitReport} disabled={submitting || !reasonCode}>
                {submitting ? 'Sending…' : 'Send report'}
              </button>
            </>
          )
        }
      >
        {done ? (
          <p>{done}</p>
        ) : (
          <div className="space-y-1.5">
            <div className="text-xs text-white/60">What happened? They won't be told who reported them.</div>
            {reasons.map((opt) => {
              const selected = reasonCode === opt.value
              return (
                <button
                  key={opt.value}
                  type="button"
                  aria-pressed={selected}
                  onClick={() => setReasonCode(opt.value)}
                  className={[
                    'w-full flex items-center gap-2.5 text-left rounded-lg border px-3 py-2.5 text-sm transition',
                    selected ? 'border-white bg-white/10' : 'border-white/15 hover:border-white/30',
                  ].join(' ')}
                >
                  <span
                    className={[
                      'shrink-0 h-4 w-4 rounded-full border grid place-items-center',
                      selected ? 'border-white' : 'border-white/40',
                    ].join(' ')}
                  >
                    {selected && <span className="h-2 w-2 rounded-full bg-white" />}
                  </span>
                  <span>{opt.label}</span>
                </button>
              )
            })}
            <textarea
              className="w-full bg-transparent outline-none border border-white/10 focus:border-white/30 rounded px-2 py-2 min-h-[64px] text-accent resize-none"
              placeholder="Anything else we should know? (optional, only the HO:RA team sees this)"
              value={details}
              onChange={(e) => setDetails(e.target.value)}
              maxLength={1000}
              onKeyDown={(e) => e.stopPropagation()}
            />
            {error && <p className="mt-2 text-red-400 text-sm">{error}</p>}
          </div>
        )}
      </Modal>

      <Modal
        open={mode === 'block'}
        onClose={close}
        title={done ? 'Blocked' : blockConfirmTitle(userName)}
        actions={
          done ? (
            <button className="px-3 py-1.5 rounded-md bg-white/90 text-black hover:bg-white" onClick={close}>
              Done
            </button>
          ) : (
            <>
              <button className={btn} onClick={close} disabled={submitting}>
                Cancel
              </button>
              <button className={danger} onClick={submitBlock} disabled={submitting}>
                {submitting ? 'Blocking…' : 'Block'}
              </button>
            </>
          )
        }
      >
        <p>{done || BLOCK_CONFIRM_BODY}</p>
        {error && <p className="mt-2 text-red-400 text-sm">{error}</p>}
      </Modal>
    </div>
  )
}
