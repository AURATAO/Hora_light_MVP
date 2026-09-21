import { useEffect, useMemo, useRef, useState } from 'react'
import { approvedBudgetCentsFor, needsReceipt } from '../lib/taskBudget'
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { api, API_BASE } from '../api/client'
import TaskChatBox from '../components/TaskChatBox'
import CancelTaskButton from '../components/CancelTaskButton'
import { useAuth } from '../auth/AuthContext'
import UserPill from '../components/UserPill'
import LiveTrackingCard from '../components/LiveTrackingCard'
import { broadcastPhase, shouldPollLive } from '../lib/liveTracking'
import {
  GAP_LABEL,
  LAUNDRY_WAIT_HINT,
  PAUSED_REQUESTER,
  PAUSED_SUPPORTER,
  SUPPORTER_CANCELLED_NOTE,
  buildSessionTimeline,
  gapsNote,
  isPaused as isPausedBetweenSessions,
  showsWaitHint,
} from '../lib/workSessions'
import { gmapsPlaceUrl, gmapsDirectionsUrl } from '../utils/gmaps'
import { useLoader } from '../providers/LoaderProvider.jsx'
import PlaceInput from '../components/PlaceInput'
import { useToast } from '../providers/ToastProvider'
import { isTractionWindowActive } from '../lib/traction'
import { useTaskEstimate, formatCents } from '../hooks/useTaskEstimate'
import {
  capWarningNote,
  extensionAskLabel,
  extensionDecidedAt,
  extensionReaskLabel,
  extensionResolutionDetail,
  extensionResolutionTitle,
  holdSummary,
  timeBasisNote,
} from '../lib/paymentCopy'
import { isPayoutsOnboardingRequired } from '../api/payments'


/**
 * One itemized cost line, rendered straight from a server quote — the same
 * shape whether it came from POST /tasks/estimate (before any work is logged)
 * or from GET /tasks/:id/worklogs (after). Every number on it was computed in
 * Go; this component does no arithmetic, which is the point.
 */
function CostLine({ cost }) {
  // The verified receipt, once there is one. Distinct from
  // shopping_budget_cents, which is a ceiling and was never a charge — this is
  // the number that is actually in total_cents.
  const receipt = cost.shopping_receipt_cents || 0
  return (
    <div className="text-sm">
      Base fee <b>{formatCents(cost.base_fee_cents)}</b>
      <span className="text-white/60">
        {' '}(first {cost.included_minutes} min included) + time ({cost.billable_minutes} billable min
        {' '}× {formatCents(cost.per_minute_rate_cents)}) <b>{formatCents(cost.time_cost_cents)}</b>
      </span>
      {receipt > 0 && (
        <span className="text-white/60"> + receipt <b className="text-white">{formatCents(receipt)}</b></span>
      )}
      <span> = <b>{formatCents(cost.total_cents)}</b></span>
    </div>
  )
}

/** A timestamp as the clock on the wall, which is how a session is read back. */
function clockTime(iso) {
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return '—'
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

/** Minutes, said the way a person says them. */
function minutesLabel(minutes) {
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest === 0 ? `${hours} hr` : `${hours} hr ${rest} min`
}

/**
 * The sessions, and the free gaps between them, in order.
 *
 * ONE component for the running task and the finished settlement — the same
 * rows in both places, which is the point: a requester who watched the
 * timeline build up during the task should meet exactly that timeline again on
 * the receipt, not a different summary of it. Forking this into a separate
 * "settlement breakdown" is how the two start disagreeing.
 *
 * A gap is quieter than a session and carries its reason on its own row,
 * because "free" is the fact a reader is scanning for and it should not have to
 * be inferred from two adjacent timestamps.
 */
function SessionList({ timeline }) {
  return (
    <div className="space-y-1">
      {timeline.map(entry =>
        entry.kind === 'gap' ? (
          <div key={`gap-${entry.startAt}`} className="flex justify-between text-xs text-white/40">
            <span>{GAP_LABEL}</span>
            <span>{minutesLabel(entry.minutes)} free</span>
          </div>
        ) : (
          <div key={entry.id} className="flex justify-between text-xs">
            <span className="text-white/70">
              {clockTime(entry.startAt)} – {entry.endAt ? clockTime(entry.endAt) : 'in progress'}
            </span>
            <span className="text-white/50">
              {minutesLabel(entry.minutes)}{entry.running ? ' so far' : ''}
            </span>
          </div>
        )
      )}
    </div>
  )
}

/**
 * What was charged, itemized, for whichever side of the task is reading it.
 *
 * Every number comes from GET /tasks/:id/worklogs — the same payload the
 * running cost line above is built from, because "what does this cost" and
 * "what was I charged" are one question asked at two moments (S-05). The only
 * thing this component decides is which parts a given reader sees.
 */
// "$8.00 more" / "30 more minutes" — what an ask was for, in the words both
// the pending line and the resolution line use. Matches mobile's local
// askPhrase (mobile/src/app/task/[id].tsx) word for word.
function askPhrase(request) {
  if (!request) return ''
  if (request.kind === 'budget') return `${formatCents(request.requested_cents || 0)} more`
  return `${request.requested_minutes || 0} more minutes`
}

function SettlementPanel({ cost, settlement, timeline, isOwner, taskId }) {
  if (!cost || !settlement) return null
  // Only when the task was worked in more than one sitting: on a single-session
  // task the timeline restates the billable-minutes line above it and earns
  // nothing.
  const sessionCount = (timeline || []).filter(e => e.kind === 'session').length

  const overran =
    typeof cost.cap_minutes === 'number' &&
    typeof cost.billed_minutes === 'number' &&
    cost.total_minutes > cost.billed_minutes

  return (
    <div className="border border-white/20 rounded-md p-3 space-y-2 text-sm">
      <div className="text-xs text-white/60">
        {settlement.state === 'captured' ? 'What was charged' : 'Settlement'}
      </div>

      <div className="flex justify-between text-white/70">
        <span>Base fee <span className="text-white/40">(first {cost.included_minutes} min)</span></span>
        <span className="text-white">{formatCents(cost.base_fee_cents)}</span>
      </div>
      <div className="flex justify-between text-white/70">
        <span>{cost.billable_minutes} billable min × {formatCents(cost.per_minute_rate_cents)}</span>
        <span className="text-white">{formatCents(cost.time_cost_cents)}</span>
      </div>

      {/* WHERE THOSE MINUTES CAME FROM. The same rows the running card showed
          while the task was live, from the same builder, so the receipt agrees
          with what both parties watched being assembled. The gaps are the
          reason a requester might otherwise read "three hours on site, forty
          billable minutes" as a mistake. */}
      {sessionCount > 1 && (
        <div className="border-t border-white/10 pt-2 space-y-1">
          <div className="text-xs text-white/60">Sessions</div>
          <SessionList timeline={timeline} />
          {gapsNote(timeline) && <div className="text-xs text-white/40">{gapsNote(timeline)}</div>}
        </div>
      )}

      {/* Said plainly rather than left to be inferred from two numbers that
          disagree: the supporter worked longer than the requester agreed to
          pay for, and both of them see it in the same words. */}
      {overran && (
        <div className="text-xs text-white/40">
          {cost.total_minutes} min logged; billed to the agreed {cost.billed_minutes} min.
        </div>
      )}

      {settlement.approved_budget_cents > 0 && (
        <div className="flex justify-between text-white/70">
          <span>Receipt <span className="text-white/40">(budget {formatCents(settlement.approved_budget_cents)})</span></span>
          <span className="text-white">{formatCents(cost.shopping_receipt_cents || 0)}</span>
        </div>
      )}

      <div className="border-t border-white/10 pt-2 flex justify-between font-semibold">
        <span>{settlement.state === 'captured' ? 'Total charged' : 'Total'}</span>
        <span>{formatCents(cost.total_cents)}</span>
      </div>

      {settlement.state === 'not_charged' && (
        <div className="text-xs text-white/40">
          Nothing has been charged — payments are not switched on for this task.
        </div>
      )}
      {/* Calm on purpose, and not an action. Ops have already been emailed;
          there is nothing for either party to do, and an alarm here would send
          both of them chasing something that is already in hand. */}
      {settlement.state === 'capture_failed' && (
        <div className="text-xs text-white/40">
          We couldn&apos;t complete the payment for this task. The HO:RA team has been notified and
          will sort it out — there&apos;s nothing you need to do.
        </div>
      )}

      {/* THE DECISION TRAIL, beside the money trail. One line per mid-task
          ask: what was wanted, what was decided, and when.

          Both parties used to lose all of this the moment a task completed —
          the only record was a notification, dismissible and then gone. The
          settlement already reflects an approved increase in what was charged;
          this is what makes the decision behind that number visible.

          Absent entirely when nothing was ever asked, which is most tasks. */}
      {(settlement.requests || []).length > 0 && (
        <div className="border-t border-white/10 pt-2 space-y-1.5">
          <div className="text-xs text-white/60">Requests</div>
          {settlement.requests.map((r) => (
            <div key={r.id} className="space-y-0.5">
              <div className="flex justify-between gap-3">
                <span className="text-white/70">{extensionAskLabel(r)}</span>
                <span className={r.status === 'approved' ? 'text-white' : 'text-white/50'}>
                  {r.outcome}
                </span>
              </div>
              <div className="flex justify-between gap-3 text-xs text-white/40">
                {/* Why they asked, where they said. Never the slug. */}
                <span>{r.reason_label || '\u00a0'}</span>
                <span>{extensionDecidedAt(r)}</span>
              </div>
              {/* What actually happened when nobody answered. "No response"
                  alone is half the sentence — the useful half is that the
                  supporter's own pre-chosen fallback ran. */}
              {r.fallback && <div className="text-xs text-white/40">{r.fallback}</div>}
            </div>
          ))}
        </div>
      )}

      {/* What the SUPPORTER earned, sent only to them (server/main.go
          attaches `earned` behind an assignment check). The requester's
          version of this panel shows what they were CHARGED; this is the same
          settlement from the other side, and the two are deliberately never
          shown to the same person. */}
      {settlement.earned && (
        <div className="border-t border-white/10 pt-2 space-y-1">
          <div className="text-xs text-white/60">You earned</div>
          <div className="flex justify-between">
            <span className="text-white/70">
              {formatCents(settlement.earned.time_cents)} (time)
              {settlement.earned.reimbursement_cents > 0 && (
                <> + {formatCents(settlement.earned.reimbursement_cents)} (reimbursement)</>
              )}
            </span>
            <span className="font-semibold text-white">
              {formatCents(settlement.earned.total_cents)}
            </span>
          </div>
          {/* "On its way", never "paid": Stripe executes the transfer when the
              requester's charge settles, and the bank deposit is a further
              step on its daily payout schedule. Telling somebody the money is
              in their account when it is two days out is how support tickets
              get made. */}
          {settlement.earned.payout_status === 'paid' && (
            <div className="text-xs text-white/40">On its way to your bank.</div>
          )}
          {settlement.earned.payout_status === 'failed' && (
            <div className="text-xs text-white/40">
              We couldn&apos;t send this yet. The HO:RA team has been notified — there&apos;s nothing
              you need to do.
            </div>
          )}
        </div>
      )}

      {settlement.receipt_photo_url && (
        <div className="space-y-1">
          <div className="text-xs text-white/60">Receipt</div>
          <img
            src={settlement.receipt_photo_url}
            alt="Receipt"
            className="w-full rounded-lg object-cover max-h-64"
          />
        </div>
      )}

      {/* A mailto, not a dispute system. For beta, ops reading an email and
          fixing it by hand in the Stripe dashboard IS the process — building a
          dispute flow around it would be building the wrong thing well. Only
          the requester is offered it: they are the one who was charged. */}
      {isOwner && (
        <a
          className="inline-block text-xs text-white/60 underline hover:text-white"
          href={`mailto:support@horaapp.co?subject=${encodeURIComponent(`Problem with task ${taskId}`)}`}
        >
          Report a problem
        </a>
      )}
    </div>
  )
}

export default function TaskDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  // const { user, loading: authLoading } = useAuth()
  const { user } = useAuth()
  const { wrap } = useLoader()
  const toast = useToast()
  const [task, setTask] = useState(null)
  const [work, setWork] = useState({ items: [], total_minutes: 0, total_cost_cents: 0, has_open: false })
  const [gpsPos, setGpsPos] = useState(null)       // { lat, lng, accuracy } — assignee live
  const [gpsError, setGpsError] = useState('')
  // In flight on the "On my way" tap. Disables the button so a double-tap
  // cannot fire two requests — the server is idempotent, but a button that
  // looks unpressed after being pressed is its own bug.
  const [enrouteBusy, setEnrouteBusy] = useState(false)
  // Ticks only while a session is actually running, so the open session's row
  // in the timeline counts up. A minute is the resolution of everything on
  // this screen — the server rounds sessions to whole minutes and the cost is
  // computed from those — so a second-by-second tick would re-render sixty
  // times to change a number once.
  const [nowMs, setNowMs] = useState(() => Date.now())
  const watchIdRef = useRef(null)
  const pingIntervalRef = useRef(null)

  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)
  const [chatOpen, setChatOpen] = useState(false)

  // Complete-task modal
  const [showCompleteModal, setShowCompleteModal] = useState(false)
  const [completionPhotoURL, setCompletionPhotoURL] = useState('')
  const [completionNote, setCompletionNote] = useState('')
  const [photoUploading, setPhotoUploading] = useState(false)
  const completionPhotoInputRef = useRef(null)

  // Shopping settlement (Stripe Phase 2b). Only in play on a task with an
  // approved budget, where the server REFUSES a completion that says nothing
  // about the receipt — a client that skipped this could not complete such a
  // task at all.
  const [receiptAmount, setReceiptAmount] = useState('')
  const [receiptPhotoURL, setReceiptPhotoURL] = useState('')
  const [receiptUploading, setReceiptUploading] = useState(false)
  const receiptPhotoInputRef = useRef(null)

  // The mid-task asks, and the two numbers that come with them.
  const [extensions, setExtensions] = useState(null)
  const [extBusy, setExtBusy] = useState(false)
  const [extError, setExtError] = useState('')
  const [askAmount, setAskAmount] = useState('')
  // The preset slug, and the free text only when that slug is "other".
  const [askReason, setAskReason] = useState('')
  const [askReasonOther, setAskReasonOther] = useState('')
  const [askFallback, setAskFallback] = useState('')
  const [askFallbackNote, setAskFallbackNote] = useState('')
  // The re-ask form, after a resolution. Collapsed until the supporter asks
  // for it: the resolution is what leads that card, and the full budget form
  // sitting open underneath it is what made the screen read as a nudge to
  // re-ask somebody who had just said no.
  const [askReopened, setAskReopened] = useState(false)
  // The UNRELATED kind of ask, behind a disclosure for the same reason.
  const [showOtherKind, setShowOtherKind] = useState(false)

  // The payouts gate: a supporter tried to accept and has not set up payouts.
  // Holds the backend's own wording so the prompt cannot drift from the
  // refusal that produced it.
  const [payoutsGate, setPayoutsGate] = useState(null)

  // Travel time estimate (supporter only, after task is accepted)
  const [travelEst, setTravelEst] = useState(null)   // { travel_minutes, task_minutes, total_minutes }
  const [travelLoading, setTravelLoading] = useState(false)
  const travelCalledRef = useRef(false)



  // 編輯表單狀態
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [category, setCategory] = useState('task')
  const [locations, setLocations] = useState([{ label: '' }])
  const [minutes, setMinutes] = useState(30)
  const [prepay, setPrepay] = useState('')
  const [mode, setMode] = useState('now')
  const [date, setDate] = useState('')
  const [timeStr, setTimeStr] = useState('')

  // Auto-fetch travel estimate for the assignee (supporter) once task is accepted
  useEffect(() => {
    if (!task?.assigned_to_id) return
    if (!user?.id || task.assigned_to_id !== user.id) return
    if (travelCalledRef.current) return

    // If already stored in task, use it directly
    if (task.travel_time_minutes != null && task.total_estimate_minutes != null) {
      setTravelEst({
        travel_minutes: task.travel_time_minutes,
        task_minutes: task.estimated_minutes,
        total_minutes: task.total_estimate_minutes,
      })
      travelCalledRef.current = true
      return
    }

    if (!navigator.geolocation) return
    travelCalledRef.current = true
    setTravelLoading(true)

    navigator.geolocation.getCurrentPosition(
      async (pos) => {
        try {
          const res = await api(`/tasks/${task.id}/estimate-travel`, {
            method: 'POST',
            body: {
              supporter_lat: pos.coords.latitude,
              supporter_lng: pos.coords.longitude,
            },
          })
          setTravelEst(res)
        } catch {
          // silently ignore — GPS denied or Maps error
        } finally {
          setTravelLoading(false)
        }
      },
      () => setTravelLoading(false), // GPS denied
      { timeout: 8000 }
    )
  }, [task?.assigned_to_id, task?.id, user?.id])

  // ?review=true → redirect to review page (for old completion emails)
  useEffect(() => {
    if (!task || !user) return
    const params = new URLSearchParams(location.search)
    if (params.get('review') !== 'true') return
    if (task.status !== 'completed') return
    if (task.requester_id !== user.id) return
    navigate(`/tasks/${id}/review`, { replace: true })
  }, [task, user, location.search, id, navigate])

  const locs = (() => {
    // 1) 嘗試讀 locations_geo（支援 string / null / []）
    let geo = task?.locations_geo
    if (typeof geo === 'string') {
      try { geo = JSON.parse(geo) } catch { geo = [] }
    }
    if (Array.isArray(geo) && geo.length > 0) {
      // 只保留有 label 或座標/ID 的點
      return geo.filter(p => p && (p.label || p.placeId || p.id || (p.lat != null && p.lng != null)))
    }

    // 2) 回退到舊的 location_text（用 ' | ' 分隔）
    const labels = (task?.location_text || '')
      .split(' | ')
      .map(s => s.trim())
      .filter(Boolean)

    return labels.map(label => ({ label }))
  })()

  // ✅ 單一 effect：等 auth ready 再抓 任務 + 工時
  useEffect(() => {
    // if (authLoading || !user || !id) return
    if (!user || !id) return
    let alive = true
      // ;(async () => {
      //   setLoading(true)
      //   setError('')
      //   try {
      //     const t = await api(`/tasks/${id}`)
      //     if (!alive) return
      //     setTask(t)

      //     // 作者或接單者可看工時；403 就忽略
      //     try {
      //       const w = await api(`/tasks/${id}/worklogs`)
      //       if (alive) setWork(w)
      //     } catch {/* ignore */}
      //   } catch (e) {
      //     if (alive) setError(e.message || 'Failed to load')
      //   } finally {
      //     if (alive) setLoading(false)
      //   }
      // })()
      ; (async () => {
        setError('')
        await wrap(async () => {
          const t = await api(`/tasks/${id}`)
          if (!alive) return
          setTask(t)
          // Worklogs are visible only to the requester or the assignee. Only
          // fetch them when the viewer is a party — otherwise the backend 403s
          // (e.g. a supporter browsing an unaccepted task), which is just
          // console noise since a non-party has no worklogs to show anyway.
          const isParty = !!user?.id && (t.requester_id === user.id || t.assigned_to_id === user.id)
          if (isParty) {
            try {
              const w = await api(`/tasks/${id}/worklogs`)
              if (alive) setWork(w)
            } catch {/* ignore */ }
            try {
              const x = await api(`/tasks/${id}/extensions`)
              if (alive) setExtensions(x)
            } catch {/* ignore */ }
          }
        }).catch((e) => {
          if (alive) setError(e.message || 'Failed to load')
        })
      })()
    return () => { alive = false }
  }, [user, id])

  function normalizeLocationItem(x) {
    if (!x) return { label: '' }
    if (typeof x === 'string') return { label: x }
    if (typeof x === 'object') {
      const label = x.label || x.description || x.formatted || ''
      return { ...x, label }
    }
    return { label: String(x) }
  }

  function addLocation() {
    setLocations(prev => [...prev, { label: '' }])
  }

  function updateLocation(i, v) {
    setLocations(prev =>
      prev.map((x, idx) => (idx === i ? normalizeLocationItem(v) : x))
    )
  }

  function removeLocation(i) {
    setLocations(prev => prev.filter((_, idx) => idx !== i))
  }

  // (`isActivelyWorking` lived here. It was half of the pair that decided
  // whether to broadcast, and both halves are now inside broadcastPhase below,
  // where they are shared with mobile and tested.)

  // Whether the live card should be on screen at all. One predicate, shared
  // with mobile (app/src/lib/liveTracking.js), so the two clients cannot drift
  // into polling different sets of tasks — and matching exactly what the
  // server will answer, since GET /tasks/:id/live 404s outside this set.
  const canWatchLive = shouldPollLive({
    isRequester: Boolean(user?.id && task?.requester_id && user.id === task.requester_id),
    status: task?.status,
    assignedToId: task?.assigned_to_id,
  })

  // Assignee, before the clock starts: has this supporter tapped "On my way"?
  // The window runs from that tap until the first clock-in, and the server
  // accepts source='enroute' pings for exactly that span — see
  // enrouteWindowOpen in server/live_tracking.go.
  //
  // The window requires ZERO worklogs, not merely no OPEN one — the same rule
  // the server enforces. enroute_at is never cleared, so a window keyed on that
  // column alone would re-open after clock-out and keep sharing the supporter's
  // position for the rest of the task's life. Counting rows, not minutes: a
  // session that logged no billable time is still a clock-in.
  const isAssigneeHere = Boolean(user?.id && task?.assigned_to_id && user.id === task.assigned_to_id)
  const enrouteWindowOpen = Boolean(
    isAssigneeHere && task?.status === 'open' && (work.items?.length ?? 0) === 0
  )
  const isEnrouteSharing = Boolean(enrouteWindowOpen && task?.enroute_at)
  const canGoEnroute = Boolean(enrouteWindowOpen && !task?.enroute_at)

  // Which phase this device is broadcasting in, if any. One predicate, shared
  // with mobile and tested (lib/liveTracking.js broadcastPhase), replacing the
  // pair of inline booleans that used to decide it here and a second pair that
  // decided it there.
  //
  // What it settles for multi-session: a PAUSED supporter broadcasts nothing.
  // 'working' is gone with the open worklog and 'enroute' does not come back
  // to fill the hole, because it requires ZERO worklogs rather than merely no
  // open one — enroute_at is never cleared, so a window keyed on that column
  // alone would re-open at every clock-out and keep sharing a position through
  // every gap in the task.
  const gpsPhase = broadcastPhase({
    isAssignee: isAssigneeHere,
    status: task?.status,
    hasOpenWorklog: Boolean(work.has_open),
    sessionCount: work.items?.length ?? 0,
    enrouteAt: task?.enroute_at,
  })

  // Assignee: watch GPS + ping backend every 30s while clocked in, and — once
  // they have said they are on their way — for the trip there too. One effect
  // rather than two, because the browser only has one geolocation watch worth
  // holding and the two phases differ in nothing but the `source` they send.
  useEffect(() => {
    const sharing = gpsPhase !== 'none'
    const source = gpsPhase === 'working' ? 'foreground' : 'enroute'
    if (!sharing) {
      if (watchIdRef.current != null) {
        navigator.geolocation.clearWatch(watchIdRef.current)
        watchIdRef.current = null
      }
      clearInterval(pingIntervalRef.current)
      pingIntervalRef.current = null
      return
    }
    if (!navigator.geolocation) {
      setGpsError('GPS not supported on this device')
      return
    }
    let lastPos = null
    watchIdRef.current = navigator.geolocation.watchPosition(
      (pos) => {
        lastPos = { lat: pos.coords.latitude, lng: pos.coords.longitude, accuracy: Math.round(pos.coords.accuracy) }
        setGpsPos(lastPos)
        setGpsError('')
      },
      (err) => setGpsError(err.message),
      { enableHighAccuracy: true, maximumAge: 5000 }
    )
    // ping backend every 30s
    pingIntervalRef.current = setInterval(async () => {
      if (!lastPos) return
      try {
        await api(`/tasks/${id}/gps-ping`, {
          method: 'POST',
          body: { lat: lastPos.lat, lng: lastPos.lng, accuracy: lastPos.accuracy, source },
        })
      } catch { /* silent — don't interrupt UX */ }
    }, 30_000)
    return () => {
      if (watchIdRef.current != null) {
        navigator.geolocation.clearWatch(watchIdRef.current)
        watchIdRef.current = null
      }
      clearInterval(pingIntervalRef.current)
      pingIntervalRef.current = null
    }
  }, [gpsPhase, id])

  // The requester's poll now lives in LiveTrackingCard, which owns both the
  // request and the "stop when the tab is backgrounded" rule the old 30s
  // interval here never had.

  // ✅ 用 UUID 判斷身分
  const isOwner = Boolean(user?.id && task?.requester_id && user.id === task.requester_id)
  // The requester sees their own words; everyone else sees the preset's label
  // or nothing at all. cancel_reason is free text and may have been typed by
  // an ops admin about the person reading it.
  const cancellationReason = isOwner
    ? (task?.cancel_reason_label ?? task?.cancel_reason ?? null)
    : (task?.cancel_reason_label ?? null)
  const isAssignee = Boolean(user?.id && task?.assigned_to_id && user.id === task.assigned_to_id)
  const hasLogged = (work.total_minutes || 0) > 0
  const canComplete = Boolean((isOwner || isAssignee) && task?.status === 'open' && !!task?.assigned_to_id && !work.has_open && hasLogged)
  const canAccept = Boolean(!isOwner && !isAssignee && task?.status === 'open' && !task?.assigned_to_id)

  // ── Multi-session ─────────────────────────────────────────────────────────
  //
  // The sessions and the free gaps between them. `work.items` is the raw wire
  // shape, where the columns are spelled `start`/`end` — normalized here, at
  // the one place the payload meets the timeline builder, so the shared module
  // (and its tests, and mobile's copy of it) never has to know either spelling.
  const sessions = (work.items || []).map(w => ({
    id: w.id,
    startAt: w.start,
    endAt: w.end ?? null,
  }))
  const timeline = buildSessionTimeline(sessions, { nowMs })
  // Clocked out with the task still live — a pause, not an ending. The screen
  // used to treat the first clock-out as terminal for the supporter; this is
  // what makes it a state rather than a stop.
  const paused = isPausedBetweenSessions({
    hasOpenWorklog: Boolean(work.has_open),
    sessionCount: sessions.length,
    status: task?.status,
  })
  const showWaitHint = Boolean(isAssignee && task?.status === 'open' && showsWaitHint(task?.category))

  // For the length of the Traction 3 round the questionnaire replaces the
  // classic review form, and the supporter gets one of their own — the only
  // time this side of a completed task is asked anything. When the window
  // passes both revert on their own: the flag is a date check, nothing else.
  const questionnaireActive = isTractionWindowActive()
  const canReview = Boolean(isOwner && task?.status === 'completed' && task?.assigned_to_id)
  const canGiveFeedback = Boolean(isAssignee && task?.status === 'completed' && questionnaireActive)

  // async function reloadWorkAndTask() {
  //   const [t, w] = await Promise.all([
  //     api(`/tasks/${id}`),
  //     api(`/tasks/${id}/worklogs`).catch(() => work),
  //   ])
  //   setTask(t)
  //   setWork(w)
  // }
  async function goEnroute() {
    setEnrouteBusy(true)
    try {
      await api(`/tasks/${id}/enroute`, { method: 'POST' })
      // Reload rather than patching state locally: enroute_at comes back on
      // the task, and it is what both the button and the ping loop read.
      await reloadWorkAndTask()
    } catch (e) {
      alert(e.message || "Couldn't start sharing your location.")
    } finally {
      setEnrouteBusy(false)
    }
  }

  async function reloadWorkAndTask() {
    await wrap(async () => {
      const [t, w] = await Promise.all([
        api(`/tasks/${id}`),
        api(`/tasks/${id}/worklogs`).catch(() => work),
      ])
      setTask(t)
      setWork(w)
    })
  }

  async function acceptFromDetail() {
    // try {
    //   await api(`/tasks/${id}/accept`, { method: 'POST' })
    //   await reloadWorkAndTask()
    // } catch (e) {
    //   alert(e.message || 'Failed to accept')
    // }
    await wrap(async () => {
      try {
        await api(`/tasks/${id}/accept`, { method: 'POST' })
        await reloadWorkAndTask()
      } catch (e) {
        if (isPayoutsOnboardingRequired(e)) {
          // Not an error and not a race — the payouts gate. The supporter can
          // do this task; they just have nowhere for the money to land yet.
          // Sending them straight to Earnings is the only useful response,
          // and the task is still open when they come back because the gate
          // refuses BEFORE the claim.
          setPayoutsGate(e?.body?.message || '')
        } else if (e?.body?.error === 'not available') {
          // Lost the race — someone else accepted first. Pull the fresh state
          // so the Accept button disappears.
          toast('This task was just accepted by someone else.')
          await reloadWorkAndTask()
        } else {
          toast(e?.body?.error || e.message || 'Failed to accept')
        }
      }
    })
  }

  async function clockIn() {
    // try {
    //   await api(`/tasks/${id}/clock-in`, { method: 'POST' })
    //   await reloadWorkAndTask()
    // } catch (e) {
    //   alert(e.message || 'Clock in failed')
    // }
    await wrap(async () => {
      try {
        await api(`/tasks/${id}/clock-in`, { method: 'POST' })
        await reloadWorkAndTask()
      } catch (e) {
        toast(e.message || 'Clock in failed')
      }
    })
  }
  async function clockOut() {
    // try {
    //   await api(`/tasks/${id}/clock-out`, { method: 'POST' })
    //   await reloadWorkAndTask()
    // } catch (e) {
    //   alert(e.message || 'Clock out failed')
    // }
    await wrap(async () => {
      try {
        await api(`/tasks/${id}/clock-out`, { method: 'POST' })
        await reloadWorkAndTask()
      } catch (e) {
        toast(e.message || 'Clock out failed')
      }
    })
  }

  // One uploader for both photos. The receipt goes through the same endpoint
  // and the same bucket as the completion photo — same kind of file, same
  // moment, same flow — so a second storage path would be a second thing to
  // keep working for no gain.
  async function uploadCompletionPhoto(file, setURL = setCompletionPhotoURL, setBusy = setPhotoUploading) {
    if (!file) return
    if (file.size > 5 * 1024 * 1024) {
      toast('Max 5MB — please choose a smaller image', 'error')
      return
    }
    setBusy(true)
    try {
      const fd = new FormData()
      fd.append('file', file)

      // Re-use the same auth pattern as api() in client.js
      const { supabase } = await import('../lib/supabaseClient')
      const { data: { session } } = await supabase.auth.getSession()
      const token = session?.access_token || null

      const headers = {}
      if (token) headers['Authorization'] = `Bearer ${token}`

      console.log('[uploadCompletionPhoto] task=%s file=%s size=%d', id, file.name, file.size)

      const res = await fetch(`${API_BASE}/tasks/${id}/completion-photo`, {
        method: 'POST',
        credentials: 'include',
        headers,
        body: fd,
      })
      const data = await res.json().catch(() => ({}))
      console.log('[uploadCompletionPhoto] status=%d body=', res.status, data)
      if (!res.ok) throw new Error(data?.error || `Upload failed (${res.status})`)
      setURL(data.url)
    } catch (err) {
      console.error('[uploadCompletionPhoto] error:', err)
      toast(err.message || 'Photo upload failed', 'error')
    } finally {
      setBusy(false)
    }
  }

  async function markCompleted() {
    await wrap(async () => {
      try {
        const completeBody = { completion_photo_url: completionPhotoURL, completion_note: completionNote }
        // Sent only on a task that actually has a budget. Elsewhere the field
        // is absent rather than 0: the server reads an absent receipt on a
        // shopping task as "you haven't told us", which is the whole point of
        // the distinction, and sending a spurious 0 on every other task would
        // put a receipt of record on tasks nobody shopped for.
        if (approvedBudgetCents > 0) {
          completeBody.receipt_amount_cents = receiptCents
          if (receiptPhotoURL) completeBody.receipt_photo_url = receiptPhotoURL
        }
        console.log('[completeTask] body:', completeBody)
        await api(`/tasks/${id}/complete`, {
          method: 'POST',
          body: completeBody,
        })
        setShowCompleteModal(false)
        // During the round, completing is the moment the supporter answers:
        // nothing else on web prompts them. Outside it, unchanged.
        navigate(questionnaireActive ? `/tasks/${id}/review` : '/my?tab=done', { replace: true })
      } catch (e) {
        // The one completion failure the supporter can fix from here: an
        // over-budget receipt means "ask for an increase, or correct the
        // amount", and the server's message says which. Surfaced verbatim
        // rather than flattened into "Complete failed".
        toast(e?.body?.message || e.message || 'Complete failed')
      }
    })
  }

  // ── Mid-task asks (Stripe Phase 2b) ──────────────────────────────────────
  //
  // Loaded on every reload and polled while the task is live. The poll is
  // load-bearing rather than cosmetic: the server applies the five-minute
  // expiry on every read of this list, so polling is simultaneously how the
  // answer arrives and how "no answer" becomes an answer at all.
  // A NEW RESOLUTION SUPERSEDES THE LAST ONE, so both disclosures close with
  // it. Without this, a supporter who opened the re-ask form, sent it, and was
  // refused again would meet the form already open under the new answer —
  // which is the exact weighting this whole change removes.
  //
  // Keyed off the raw list rather than off `latestAsk`, which is derived 200
  // lines below this and would be in its temporal dead zone here. The two
  // fields are the same ones latestAsk is read for; the derived value is a
  // convenience for the JSX, not a source of truth.
  const items = extensions?.items || []
  const newestAsk = items.length ? items[items.length - 1] : null
  const newestAskID = newestAsk?.id ?? null
  const newestAskStatus = newestAsk?.status ?? null
  useEffect(() => {
    setAskReopened(false)
    setShowOtherKind(false)
  }, [newestAskID, newestAskStatus])

  async function loadExtensions() {
    try {
      setExtensions(await api(`/tasks/${id}/extensions`))
    } catch {
      // Silent — this runs on a timer, and a banner that flickers every few
      // seconds is worse than a stale card.
    }
  }

  async function sendBudgetAsk() {
    const dollars = Number(String(askAmount).replace(/[^0-9.]/g, ''))
    if (!Number.isFinite(dollars) || dollars <= 0) {
      setExtError('Enter how much more you need.')
      return
    }
    if (!askReason) {
      setExtError('Pick a reason.')
      return
    }
    if (askReason === 'other' && !askReasonOther.trim()) {
      setExtError('Say briefly what happened.')
      return
    }
    if (!askFallback) {
      setExtError("Choose what to do if there's no answer.")
      return
    }
    // The stored shape: a preset slug, or "other: <what they typed>". The
    // server maps it back to a sentence for the requester's approval card.
    const reason =
      askReason === 'other' ? `other: ${askReasonOther.trim()}` : askReason
    setExtBusy(true)
    setExtError('')
    try {
      await api(`/tasks/${id}/budget-increase`, {
        method: 'POST',
        body: {
          requested_cents: Math.round(dollars * 100),
          reason,
          fallback: askFallback,
          fallback_note: askFallbackNote || undefined,
        },
      })
      setAskAmount(''); setAskReason(''); setAskReasonOther('')
      setAskFallback(''); setAskFallbackNote('')
      await loadExtensions()
    } catch (e) {
      setExtError(e?.body?.message || e.message || "Couldn't send your request.")
    } finally {
      setExtBusy(false)
    }
  }

  async function sendTimeAsk(minutes) {
    setExtBusy(true)
    setExtError('')
    try {
      await api(`/tasks/${id}/time-extension`, { method: 'POST', body: { requested_minutes: minutes } })
      await loadExtensions()
    } catch (e) {
      setExtError(e?.body?.message || e.message || "Couldn't send your request.")
    } finally {
      setExtBusy(false)
    }
  }

  // The requester's one click. A 409 is not their mistake — the request timed
  // out, or their phone already answered it — so the refetch is what actually
  // resolves the card and the message only says what happened.
  async function resolveAsk(extensionId, decision) {
    setExtBusy(true)
    setExtError('')
    try {
      await api(`/tasks/${id}/extensions/${extensionId}/${decision}`, { method: 'POST' })
      await Promise.all([loadExtensions(), reloadWorkAndTask()])
    } catch (e) {
      setExtError(e?.body?.message || e.message || "Couldn't send your answer.")
      await loadExtensions()
    } finally {
      setExtBusy(false)
    }
  }

  // 進入編輯模式時把 task 值灌入表單（與你原本相同邏輯）
  function startEdit() {
    if (!task) return

    setTitle(task.title || '')
    setDescription(task.description || '')
    setCategory(task.category || 'task')

    // 1) 先看 locations_geo（可能是 jsonb 或字串）
    let locItems = []
    let geo = task.locations_geo

    if (typeof geo === 'string') {
      try { geo = JSON.parse(geo) } catch { geo = [] }
    }

    if (Array.isArray(geo) && geo.length > 0) {
      locItems = geo.map(normalizeLocationItem)
    } else {
      // 2) 沒有 geo → 用 location_text
      const labels = (task.location_text || '')
        .split(' | ')
        .map(s => s.trim())
        .filter(Boolean)

      locItems = labels.length
        ? labels.map(normalizeLocationItem)
        : [{ label: '' }]
    }

    setLocations(locItems)

    setMinutes(task.estimated_minutes || 30)
    setPrepay(((task.prepay_amount_cents || 0) / 100).toString())

    if (task.is_immediate) {
      setMode('now')
      setDate('')
      setTimeStr('')
    } else if (task.scheduled_at) {
      const d = new Date(task.scheduled_at)
      const pad = (n) => String(n).padStart(2, '0')
      setMode('schedule')
      setDate(`${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`)
      setTimeStr(`${pad(d.getHours())}:${pad(d.getMinutes())}`)
    } else {
      setMode('now')
      setDate('')
      setTimeStr('')
    }

    setEditing(true)
  }


  const scheduledAtISO = useMemo(() => {
    if (mode === 'schedule' && date && timeStr) return new Date(`${date}T${timeStr}`).toISOString()
    return ''
  }, [mode, date, timeStr])

  const advance = useMemo(() => {
    if (prepay === '') return 0
    const n = Number(prepay)
    return Number.isNaN(n) ? 0 : Math.max(0, n)
  }, [prepay])
  // (A `totalEUR` local used to live here. It held dollars — the product has
  // never billed in euros — and is gone with the last of the local money math.)
  const totalDuration = useMemo(() => {
    const m = work.total_minutes || 0
    if (m === 0) return '0 min'
    const h = Math.floor(m / 60)
    const rem = m % 60
    if (h === 0) return `${rem} min`
    if (rem === 0) return `${h}h`
    return `${h}h ${rem}min`
  }, [work.total_minutes])

  // Two different questions, two different server answers — neither computed
  // here. Once time is logged, the authoritative figure is the settlement the
  // worklogs endpoint returns (work.cost); before that, the only honest number
  // is a quote against the requester's estimate.
  //
  // This replaced a local useMemo that carried its own copy of the fee
  // schedule (25 / 18 / 12 and `minutes * 0.50`), which is exactly what S-05
  // forbids: the backend could not change a fee without this page quoting the
  // old one to the person being charged.
  const estimatedCost = useTaskEstimate({
    category: task?.category,
    estimatedMinutes: task?.estimated_minutes,
    enabled: Boolean(task?.category) && !hasLogged,
  })
  const costBreakdown = hasLogged ? work.cost : estimatedCost

  // ── Stripe Phase 2b, derived once ───────────────────────────────────────
  const settlement = work?.settlement || null
  const capState = settlement?.time_cap || null
  // Freshest source first, always-present source last — see taskBudget.js.
  // Never the live payments_enforced flag: this task's budget is a fact about
  // the task, not about what posting requires today.
  const approvedBudgetCents = approvedBudgetCentsFor({ extensions, settlement, task })
  const toleranceCents = extensions?.tolerance_cents ?? 0
  const pendingAsk = (extensions?.items || []).find(e => e.status === 'pending') || null
  const latestAsk = (extensions?.items || []).slice(-1)[0] || null
  // A resolved ask is worth showing until it is superseded, and above all when
  // it was DENIED or EXPIRED — that is the moment the supporter's own fallback
  // becomes the instruction.
  const askResolved = Boolean(
    !pendingAsk && latestAsk && (latestAsk.status === 'denied' || latestAsk.status === 'expired')
  )
  // The OTHER kind of ask, kept behind a disclosure after a resolution. A
  // denied budget request says nothing about whether the job needs more time,
  // so the option stays reachable — it simply is not the answer to what was
  // just asked.
  const otherKindAvailable = latestAsk?.kind === 'time'
    ? approvedBudgetCents > 0
    : Boolean((capState?.warning || capState?.reached) && (extensions?.time_choices || []).length > 0)
  const isTaskActive = task?.status === 'open' && Boolean(task?.assigned_to_id)
  // Whether the itemized settlement card below is going to render. The cost
  // card's one-line "Final cost" is redundant next to it — the two sat
  // adjacent showing the same number — so that line defers to this.
  // THE SERVER DECIDES WHO SEES A SETTLEMENT, not this line.
  //
  // It used to also require (isOwner || isAssignee) — and a cancel NULLs
  // assigned_to_id, so the supporter of a cancelled task failed both halves
  // and saw no payment record for a task they had just been paid for. The
  // settlement endpoint authorizes the requester, the assignee, anyone who
  // logged time, and (since the cancellation billing policy) whoever was on
  // the task when it was cancelled. A second, weaker copy of that rule here
  // could only ever hide something the server had already agreed to send.
  const showSettlementPanel = task?.status !== 'open' && Boolean(work?.cost && settlement)

  // The hold on the requester's card. Server-attached and requester-only — the
  // key is absent from the supporter's copy of this task, so this is null for
  // them by construction rather than by a check here.
  const holdLine = holdSummary(task?.payment)
  const receiptCents = (() => {
    const n = Number(String(receiptAmount).replace(/[^0-9.]/g, ''))
    return Number.isFinite(n) && n >= 0 ? Math.round(n * 100) : 0
  })()

  // Poll the mid-task asks while the task is live. Five seconds, because this
  // is the only clock either party has on a five-minute approval window — and
  // because the server expires a stale request on each read, so the poll is
  // what makes "no answer" resolve at all.
  //
  // THIS HOOK MUST STAY BELOW THE DERIVED CONSTS ABOVE. A dependency array is
  // evaluated during render, at the point the useEffect CALL appears — not
  // when the effect body runs — so a `const` declared further down the
  // component is in its temporal dead zone at that moment. This effect was
  // originally written up beside the other useEffects, ~440 lines above
  // `isTaskActive`, and every render of this page threw
  // "Cannot access 'isTaskActive' before initialization" before any of it
  // reached the screen. Nothing about the effect's body was wrong; the
  // position of the call was.
  //
  // Hook order is unaffected: there is no early return anywhere above this
  // point, so it still runs unconditionally on every render.
  useEffect(() => {
    if (!isTaskActive || !user?.id) return
    const isParty = task?.requester_id === user.id || task?.assigned_to_id === user.id
    if (!isParty) return
    const timer = setInterval(loadExtensions, 5000)
    return () => clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isTaskActive, user?.id, task?.requester_id, task?.assigned_to_id, id])

  // The running session's clock. Declared here, below `work`, for the same
  // temporal-dead-zone reason as the effect above — and gated on an actually
  // open session so a finished task's tab is not re-rendering once a minute
  // forever.
  useEffect(() => {
    if (!work.has_open) return
    const timer = setInterval(() => setNowMs(Date.now()), 30000)
    return () => clearInterval(timer)
  }, [work.has_open])

  async function saveEdit() {
    await wrap(async () => {
      try {
        // 先正規化 locations
        const locItems = locations.map(normalizeLocationItem)

        // 1) 給人看的文字（後端/列表用）
        const location_text = locItems
          .map(it => (it.label || '').trim())
          .filter(Boolean)
          .join(' | ')

        // 2) 給機器用的結構（之後要落 DB 再用）
        const locations_geo = locItems
          .filter(x => (x.label || '').trim())
          .map(x => ({
            label: (x.label || '').trim(),
            placeId: x.placeId || x.id || null,
            lat: typeof x.lat === 'number' ? x.lat : null,
            lng: typeof x.lng === 'number' ? x.lng : null,
          }))

        const payload = {
          title,
          description,
          category,
          location_text,
          locations_geo,
          estimated_minutes: Number(minutes) || 30,
          prepay_amount_cents: Math.round((advance || 0) * 100),
          is_immediate: mode === 'now',
          scheduled_at: mode === 'schedule' ? scheduledAtISO : '',
        }

        const updated = await api(`/tasks/${task.id}`, {
          method: 'PATCH',
          body: payload,
        })
        setTask(updated)
        setEditing(false)
      } catch (e) {
        toast(e.message || 'Failed to save')
      }
    })
  }


  if (error) return <div className="p-6 text-red-500">{error}</div>
  if (!task) return <div className="p-6">Task not found.</div>

  const whenText = task.is_immediate ? 'ASAP' : (task.scheduled_at ? new Date(task.scheduled_at).toLocaleString() : '—')


  function DebugMe() {
    const { user } = useAuth()
    useEffect(() => {
      // 直接把目前登入者掛到 window，方便 Console 查
      window.__ME__ = { id: user?.id, email: user?.email }
      console.debug('[ME]', window.__ME__)
    }, [user])
    return null
  }

  return (
    <>
    <div className="bg-linear-to-br from-primary to-primary/30 text-accent min-h-screen py-[100px] px-4">
      <div className="mx-auto max-w-md space-y-4 border border-primary/30 backdrop-blur-md p-8 rounded-lg shadow">
        {!editing ? (
          <>
            <div className="flex items-start justify-end gap-3">

              <span className="inline-flex h-6 items-center rounded-full border border-white/15 bg-white/5 px-2 text-[11px] uppercase tracking-wide text-white/80 select-none pointer-events-none">
                {task.status}
              </span>
              {canAccept && (
                <button onClick={acceptFromDetail} className="ml-2 rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40">
                  Accept
                </button>
              )}
              {/* Editing stays open-and-unaccepted: changing the terms of a
                  job somebody has already taken is a renegotiation, and that
                  belongs in chat. */}
              {isOwner && task.status === 'open' && !task.assigned_to_id && (
                <button onClick={startEdit} className="ml-2 rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40">
                  Edit
                </button>
              )}
            </div>

            {/* The payouts gate. Shown in place of a toast because it carries
                an ACTION and a toast does not — a supporter told "set up
                payouts" with nothing to tap has been given a dead end. The
                task is still open behind this: the backend refuses before the
                claim, so nothing was taken off the board. */}
            {payoutsGate !== null && (
              <div className="mt-3 rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3 space-y-2">
                <div className="text-sm text-white">Set up payouts to start earning</div>
                <p className="text-xs text-white/60">
                  {payoutsGate || 'Add your bank details through Stripe. It only takes a couple of minutes.'}
                </p>
                <div className="flex gap-2">
                  <button
                    onClick={() => navigate('/profile/earnings')}
                    className="rounded-md px-3 py-1.5 text-xs font-semibold text-white hover:brightness-110"
                    style={{ backgroundColor: '#3A5A2D' }}
                  >
                    Set up payouts
                  </button>
                  <button
                    onClick={() => setPayoutsGate(null)}
                    className="rounded-md border border-white/20 px-3 py-1.5 text-xs text-white/70 hover:border-white/40"
                  >
                    Not now
                  </button>
                </div>
              </div>
            )}

            <h2 className="text-2xl font-semibold flex-1">{task.title}</h2>
            <div className="mt-3 grid gap-2 sm:grid-cols-2">
              <div className="flex items-center gap-3">
                <div className="text-white/70 text-sm">Requester:</div>
                <UserPill userId={task.requester_id} meId={user?.id} label="Requester" />
              </div>
              <div className="flex items-center gap-3">
                <div className="text-white/70 text-sm">Supporter:</div>
                {task.assigned_to_id
                  ? <UserPill userId={String(task.assigned_to_id)} meId={user?.id} label="Assignee" />
                  : <span className="text-white/60 text-sm">Not assigned yet</span>}
              </div>
            </div>

            <div className="text-sm text-white/80 space-y-1">
              <div><b>When:</b> {whenText}</div>
              <div><b>Estimated:</b> {task.estimated_minutes} min</div>
              {/* Only when there IS one. Rendered unconditionally, this line
                  told every companionship visit and dog walk that its
                  "Shopping budget" was $0.00 — a real number standing in for
                  "this task has no shopping in it", which is the one thing
                  paymentCopy.test.mjs pins us against. Mobile already guards
                  it this way (task/[id].tsx); web was the odd one out. */}
              {task.prepay_amount_cents > 0 && (
                <div><b>Shopping budget:</b> {formatCents(task.prepay_amount_cents)}</div>
              )}
              <div>
                <b className="block mb-2">Locations:</b>
                {locs.length ? (
                  <div className="space-y-1">
                    {locs.map((p, i) => (
                      <div key={i} className="flex items-start gap-1.5 text-sm">
                        <span>📍</span>
                        <span>
                          {locs.length > 1 && <span className="text-white/50 mr-1">Stop {i + 1}:</span>}
                          {p.label || '—'}
                        </span>
                      </div>
                    ))}
                  </div>
                ) : <span>—</span>}
                {locs.length >= 1 && (
                  <div className="mt-2">
                    <a
                      href={
                        locs.length === 1
                          ? `https://www.google.com/maps/dir/?api=1&destination=${encodeURIComponent(locs[0].label || '')}`
                          : gmapsDirectionsUrl(locs)
                      }
                      onClick={
                        isAssignee && locs.length > 1
                          ? (e) => {
                              e.preventDefault()
                              if (!navigator.geolocation) {
                                window.open(gmapsDirectionsUrl(locs), '_blank', 'noopener,noreferrer')
                                return
                              }
                              navigator.geolocation.getCurrentPosition(
                                (pos) => {
                                  const { latitude, longitude } = pos.coords
                                  const stops = locs.map(p => encodeURIComponent(p.label || '')).join('/')
                                  const url = `https://www.google.com/maps/dir/${latitude},${longitude}/${stops}`
                                  window.open(url, '_blank', 'noopener,noreferrer')
                                },
                                () => {
                                  window.open(gmapsDirectionsUrl(locs), '_blank', 'noopener,noreferrer')
                                }
                              )
                            }
                          : undefined
                      }
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-2 rounded-md border border-white/20 px-3 py-1 text-xs hover:border-white/40"
                    >
                      View Route →
                    </a>
                  </div>
                )}
              </div>
            </div>

            <div className="border border-white/20 rounded-md p-4 space-y-1 overflow-hidden">
              <div className="text-xs font-semibold uppercase tracking-wide text-white/50">Notes</div>
              <p className="text-sm leading-relaxed whitespace-pre-wrap break-words overflow-wrap-anywhere text-white/90 select-text">
                {task.description || <span className="text-white/40 italic">No notes provided.</span>}
              </p>
            </div>
            <DebugMe />

            {/* A question with a five-minute fuse on it, so it sits above the
                travel card and the cost card rather than below them — a
                requester who has to scroll to find it will not answer in time. */}
            {isOwner && isTaskActive && pendingAsk && (
              <div className="border border-white/20 rounded-md p-3 space-y-2 text-sm">
                <div className="text-xs text-white/60">
                  {pendingAsk.kind === 'budget' ? 'Budget request' : 'Time request'}
                </div>
                <div>
                  Your supporter is asking for{' '}
                  <b>
                    {pendingAsk.kind === 'budget'
                      ? `${formatCents(pendingAsk.requested_cents || 0)} more`
                      : `${pendingAsk.requested_minutes || 0} more minutes`}
                  </b>.
                </div>
                {/* reason_label, not reason: the stored value is a slug, and
                    "item_unavailable" is not something to show somebody who is
                    deciding whether to spend money. */}
                {(pendingAsk.reason_label || pendingAsk.reason) && (
                  <div className="text-white/70">{pendingAsk.reason_label || pendingAsk.reason}</div>
                )}
                {pendingAsk.kind === 'budget' && (
                  <div className="flex justify-between text-white/70">
                    <span>New budget if you approve</span>
                    <span className="text-white">
                      {formatCents(approvedBudgetCents + (pendingAsk.requested_cents || 0))}
                    </span>
                  </div>
                )}
                <div className="text-xs text-white/40">
                  No answer within {extensions?.timeout_minutes ?? 5} minutes counts as a no.
                </div>
                {extError && <div className="text-xs text-red-300">{extError}</div>}
                <div className="flex gap-2">
                  <button
                    type="button"
                    disabled={extBusy}
                    onClick={() => resolveAsk(pendingAsk.id, 'approve')}
                    className="px-3 py-1.5 text-xs rounded-lg bg-white text-black font-medium disabled:opacity-40"
                  >
                    Approve
                  </button>
                  <button
                    type="button"
                    disabled={extBusy}
                    onClick={() => resolveAsk(pendingAsk.id, 'deny')}
                    className="px-3 py-1.5 text-xs rounded-lg border border-white/20 hover:border-white/40 disabled:opacity-40"
                  >
                    Not this time
                  </button>
                </div>
              </div>
            )}

            {/* Requester: where their supporter is, right now — the #1 ask out
                of Traction 3, so it sits high, directly under the question with
                the fuse on it and above the money cards.

                It replaces what used to be here: a link labelled with the
                supporter's coordinates to five decimal places, roughly a
                one-metre box around a person. This card says how far away they
                are and draws the map; it never prints a coordinate.

                Full width, and NOT nested inside the estimated-cost box it was
                first dropped into — in there the map rendered 232px wide on a
                390px screen, three containers deep, which is not where you put
                the thing people asked for most. Mirrors mobile, where it is its
                own card above Progress. */}
            {canWatchLive && <LiveTrackingCard taskId={id} />}

            {/* The supporter's side: what they're covered for, and the two ways
                to ask for more of it. The approved budget is shown at all times
                on a shopping task, not only when something is outstanding — it
                is the number they are about to be held to at the till. */}
            {isAssignee && isTaskActive && (approvedBudgetCents > 0 || capState?.cap?.cap_minutes > 0) && (
              <div className="border border-white/20 rounded-md p-3 space-y-2 text-sm">
                <div className="text-xs text-white/60">What you&apos;re covered for</div>

                {approvedBudgetCents > 0 && (
                  <div className="flex justify-between text-white/70">
                    <span>Approved budget</span>
                    <span className="text-white">{formatCents(approvedBudgetCents)}</span>
                  </div>
                )}
                {capState?.cap?.cap_minutes > 0 && (
                  <div className="space-y-0.5">
                    <div className="flex justify-between text-white/70">
                      <span>Paid time</span>
                      <span className="text-white">
                        {capState.logged_minutes} of {capState.cap.cap_minutes} min
                      </span>
                    </div>
                    {/* Where the ceiling above comes from. Without it, 45 is a
                        second unexplained number sitting next to the 30 the
                        requester was quoted. */}
                    {timeBasisNote(capState.cap) && (
                      <div className="text-xs text-white/40">{timeBasisNote(capState.cap)}</div>
                    )}
                  </div>
                )}

                {/* Never an instruction to stop — the supporter decides when it
                    is safe to wrap up. Only a statement that the meter has. */}
                {capState?.reached ? (
                  <div className="text-xs text-white/60">
                    Time cap reached — anything past this isn&apos;t billed. Ask for more time, or wrap
                    up whenever you judge it right. You can still complete the task at any point.
                  </div>
                ) : capWarningNote(capState) ? (
                  <div className="text-xs text-white/60">{capWarningNote(capState)}</div>
                ) : null}

                {pendingAsk ? (
                  <div className="border-t border-white/10 pt-2 text-white/70">
                    Waiting on an answer:{' '}
                    {pendingAsk.kind === 'budget'
                      ? `${formatCents(pendingAsk.requested_cents || 0)} more`
                      : `${pendingAsk.requested_minutes || 0} more minutes`}.
                  </div>
                ) : askResolved ? (
                  /* THE RESOLUTION LEADS. The verdict is the quiet line and
                     the INSTRUCTION is the loud one, because what the
                     supporter needs off this card is what to do next — not a
                     restatement of what they asked. */
                  <div className="rounded-lg border border-white/15 bg-white/5 px-3 py-2.5 space-y-1">
                    <div className="text-xs text-white/50">
                      {extensionResolutionTitle(latestAsk)} · {askPhrase(latestAsk)}
                    </div>
                    <div className="text-white">{extensionResolutionDetail(latestAsk)}</div>
                  </div>
                ) : null}

                {extError && <div className="text-xs text-red-300">{extError}</div>}

                {/* AFTER A RESOLUTION, the re-ask is subdued and the
                    unrelated kind is collapsed. Requesting again is allowed by
                    design — per-kind pending re-opens the moment a request
                    resolves — but it is not what the supporter should do
                    first, and at equal weight with the fallback instruction it
                    read as the system urging them to re-ask somebody who had
                    just said no (build 11). Presentation only: nothing here
                    changes what is permitted. */}
                {!pendingAsk && askResolved && (
                  <div className="border-t border-white/10 pt-3 space-y-2">
                    <button
                      type="button"
                      disabled={extBusy || (latestAsk.kind === 'time' && (extensions?.time_choices || []).length === 0)}
                      onClick={() =>
                        latestAsk.kind === 'time'
                          ? sendTimeAsk(extensions.time_choices[0])
                          : setAskReopened(true)
                      }
                      className="text-sm text-white/60 underline hover:text-white disabled:opacity-40"
                    >
                      {extensionReaskLabel(latestAsk)}
                    </button>

                    {otherKindAvailable && (
                      <div className="space-y-2">
                        <button
                          type="button"
                          onClick={() => setShowOtherKind(v => !v)}
                          className="block text-xs text-white/50 underline hover:text-white/80"
                        >
                          {showOtherKind ? 'Fewer options' : 'More options'}
                        </button>
                        {showOtherKind && (
                          latestAsk.kind === 'time' ? (
                            <button
                              type="button"
                              disabled={extBusy}
                              onClick={() => setAskReopened(true)}
                              className="w-full rounded-lg border border-white/20 px-3 py-2.5 text-sm hover:border-white/40 disabled:opacity-40"
                            >
                              Ask for more budget
                            </button>
                          ) : (
                            <div className="grid grid-cols-2 gap-2">
                              {(extensions?.time_choices || []).map(minutes => (
                                <button
                                  key={minutes}
                                  type="button"
                                  disabled={extBusy}
                                  onClick={() => sendTimeAsk(minutes)}
                                  className="rounded-lg border border-white/20 px-3 py-3 text-sm hover:border-white/40 disabled:opacity-40"
                                >
                                  Ask for +{minutes} min
                                </button>
                              ))}
                            </div>
                          )
                        )}
                      </div>
                    )}
                  </div>
                )}

                {!pendingAsk && (!askResolved || askReopened) && (
                  <div className="border-t border-white/10 pt-4 space-y-4">
                    {approvedBudgetCents > 0 && (
                      <div className="space-y-4">
                        {/* Stacked, always. This card is used one-handed, mid
                            errand, on a phone — the amount and the reason sat
                            side by side and ran off the viewport under ~480px.
                            There is no width at which two columns here are
                            worth the overflow. */}
                        <div className="space-y-1.5">
                          <label className="block text-xs text-white/60" htmlFor="ask-amount">
                            How much more?
                          </label>
                          <input
                            id="ask-amount"
                            className="w-full rounded-lg bg-white/5 border border-white/15 px-3 py-2.5 text-base focus:outline-none focus:border-white/40"
                            placeholder="0.00"
                            inputMode="decimal"
                            value={askAmount}
                            onChange={e => setAskAmount(e.target.value)}
                          />
                        </div>

                        {/* A closed set, not a text box. Free text on a phone
                            with a five-minute timer running is a field people
                            leave empty, and an empty reason makes the
                            requester's one-tap approval a guess. */}
                        <div className="space-y-1.5">
                          <div className="text-xs text-white/60">Why?</div>
                          <div className="space-y-1.5">
                            {[...(extensions?.budget_reasons || []), { value: 'other', label: 'Other' }].map(opt => {
                              const selected = askReason === opt.value
                              return (
                                <button
                                  key={opt.value}
                                  type="button"
                                  aria-pressed={selected}
                                  onClick={() => setAskReason(opt.value)}
                                  className={[
                                    'w-full flex items-center gap-2.5 text-left rounded-lg border px-3 py-2.5 text-sm transition',
                                    selected
                                      ? 'border-white bg-white/10'
                                      : 'border-white/15 hover:border-white/30',
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
                          </div>
                          {askReason === 'other' && (
                            <input
                              className="w-full rounded-lg bg-white/5 border border-white/15 px-3 py-2.5 text-base focus:outline-none focus:border-white/40"
                              placeholder="What happened?"
                              maxLength={80}
                              value={askReasonOther}
                              onChange={e => setAskReasonOther(e.target.value)}
                            />
                          )}
                        </div>

                        {/* FALLBACK, not an action. Grouped under its own label
                            with radio styling so it reads as one choice with
                            two options — it used to sit as two buttons beside
                            the submit, three equal-looking things where only
                            one of them does anything. */}
                        <div className="space-y-1.5">
                          <div className="text-xs text-white/60">
                            If there&apos;s no answer in {extensions?.timeout_minutes ?? 5} minutes:
                          </div>
                          <div className="space-y-1.5">
                            {[
                              { value: 'buy_alternative', label: 'Buy an alternative' },
                              { value: 'skip_item', label: 'Skip this item' },
                            ].map(opt => {
                              const selected = askFallback === opt.value
                              return (
                                <button
                                  key={opt.value}
                                  type="button"
                                  aria-pressed={selected}
                                  onClick={() => setAskFallback(opt.value)}
                                  className={[
                                    'w-full flex items-center gap-2.5 text-left rounded-lg border px-3 py-2.5 text-sm transition',
                                    selected
                                      ? 'border-white bg-white/10'
                                      : 'border-white/15 hover:border-white/30',
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
                          </div>
                          {askFallback === 'buy_alternative' && (
                            <input
                              className="w-full rounded-lg bg-white/5 border border-white/15 px-3 py-2.5 text-base focus:outline-none focus:border-white/40"
                              placeholder="Which alternative?"
                              value={askFallbackNote}
                              onChange={e => setAskFallbackNote(e.target.value)}
                            />
                          )}
                        </div>

                        {/* THE submit, and visibly the only one: full width,
                            solid, below everything it acts on. */}
                        <button
                          type="button"
                          disabled={extBusy}
                          onClick={sendBudgetAsk}
                          className="w-full rounded-lg bg-white text-black font-medium px-4 py-3 text-sm hover:bg-white/90 disabled:opacity-40"
                        >
                          {extBusy ? 'Sending…' : 'Ask for more budget'}
                        </button>
                      </div>
                    )}

                    {/* Offered only once the ceiling is in sight. Before that it
                        answers a question nobody has asked. */}
                    {(capState?.warning || capState?.reached) && (extensions?.time_choices || []).length > 0 && (
                      <div className="grid grid-cols-2 gap-2">
                        {extensions.time_choices.map(minutes => (
                          <button
                            key={minutes}
                            type="button"
                            disabled={extBusy}
                            onClick={() => sendTimeAsk(minutes)}
                            className="rounded-lg border border-white/20 px-3 py-3 text-sm hover:border-white/40 disabled:opacity-40"
                          >
                            Ask for +{minutes} min
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}

            {/* THE CANCELLATION RECORD. What happened, when, and why — the
                header of the read-only view a cancelled task opens to, and
                the mirror of mobile's.

                WHY THE REASON IS NOT task.cancel_reason FOR EVERYONE. That
                column is free text written by three different callers, the
                ops panel among them. The supporter gets the PRESET'S LABEL
                and nothing else (server/cancel_reasons.go); the requester
                sees their own words back, because they are theirs. */}
            {task?.status === 'cancelled' && (
              <div className="border border-white/20 rounded-md p-3 space-y-1 text-sm">
                <div className="text-xs text-white/60">Task cancelled</div>
                {task.cancelled_at && (
                  <div className="text-xs text-white/40">
                    {new Date(task.cancelled_at).toLocaleString()}
                  </div>
                )}
                {cancellationReason && (
                  <div className="text-white">Reason: {cancellationReason}</div>
                )}
                {/* The supporter's half. A cancelled task used to vanish from
                    their lists entirely, and the notification behind it said
                    "thanks for your time" with no number — which reads as
                    "and you are getting nothing". The settlement panel below
                    says what they are actually paid. */}
                {!isOwner && (
                  <p className="text-xs text-white/40">{SUPPORTER_CANCELLED_NOTE}</p>
                )}
              </div>
            )}

            {/* What was charged, for both roles, once the task is over. */}
            {showSettlementPanel && (
              <SettlementPanel
                cost={work?.cost}
                settlement={settlement}
                timeline={timeline}
                isOwner={isOwner}
                taskId={id}
              />
            )}

            {/* THE REQUESTER'S WAY OUT, at every stage of a live task.
                There was none for an accepted task before this: the list card
                offered a cancel only while the task was unaccepted, the
                detail page offered none at all, and the server refused the
                call anyway — so ops were the only exit (build 11).

                Secondary and last in the stack. It is an exit, never this
                screen's primary action, and the dialog behind it does the
                work of saying what it will cost. */}
            {isOwner && task?.status === 'open' && (
              <div className="pt-1">
                <CancelTaskButton
                  taskId={id}
                  variant="button"
                  label="Cancel task"
                  onDone={() => reloadWorkAndTask()}
                />
              </div>
            )}

            {/* What is reserved, for the requester of a live task. The whole
                failure this addresses is an off-session pre-auth being silent:
                a requester who cannot see that money was held assumes the post
                failed and cancels it. Gone once the task closes — the
                settlement card then says what became of it. */}
            {isOwner && holdLine && task?.status === 'open' && (
              <div className="border border-white/20 rounded-md p-3 space-y-1 text-sm">
                <div className="flex justify-between">
                  <span className="text-white/70">On hold</span>
                  <span className="text-white font-medium">{holdLine}</span>
                </div>
                <p className="text-xs text-white/40">
                  Not a charge. You&apos;re billed for actual time and purchases when the task
                  completes, and anything unused is released automatically.
                </p>
                {/* Deliberately NOT repeating the CostLine here: the estimate
                    card directly below already renders it from the same server
                    quote, and two identical breakdowns stacked on one screen
                    read as two different numbers being described. This card
                    answers only what that one cannot — what is reserved, on
                    which card, and that it is not a charge. */}
                {task?.prepay_amount_cents > 0 && (
                  <p className="text-xs text-white/40">
                    The hold also covers up to {formatCents(task.prepay_amount_cents)} of shopping,
                    reimbursed against the receipt.
                  </p>
                )}
              </div>
            )}

            {/* Travel estimate card — shown to assignee only */}
            {isAssignee && task.assigned_to_id && (travelLoading || travelEst) && (
              <div className="border border-white/20 rounded-md p-3 space-y-2 text-sm">
                {travelLoading ? (
                  <div className="text-white/50 text-xs">Calculating travel time…</div>
                ) : travelEst && (
                  <>
                    <div className="flex justify-between text-white/70">
                      <span>Expected duration</span>
                      <span className="text-white font-medium">
                        {task.estimated_minutes} min
                      </span>
                    </div>
                    <a
                      href={`https://www.google.com/maps/dir/?api=1&destination=${encodeURIComponent(task.location_text || '')}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="flex justify-between text-white/70 hover:text-white transition-colors"
                    >
                      <span>Travel time</span>
                      <span className="text-white font-medium">~ {travelEst.travel_minutes} min driving ↗</span>
                    </a>
                    <div className="border-t border-white/10 pt-2 flex justify-between font-semibold">
                      <span>Total estimate</span>
                      <span>~ {travelEst.total_minutes} min</span>
                    </div>
                  </>
                )}
              </div>
            )}

            {/* The running-cost card. Hidden once the itemized settlement card
                is showing: every other child of this container is gated on
                `status === 'open'`, so on a settled task it would render as an
                empty bordered box under the settlement. */}
            {(isOwner || isAssignee) && !showSettlementPanel && (
              <div className="border border-white/20 rounded-md p-3 space-y-2">
                <div className="flex items-center justify-between">
                  <div>
                    {/* The requester's half of the pause, ABOVE the numbers so
                        it is read before the total is — matching where the
                        supporter's own line sits on their copy of this card,
                        and where mobile puts both. */}
                    {isOwner && paused && (
                      <div className="mb-2 rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm text-white/70">
                        {PAUSED_REQUESTER}
                      </div>
                    )}
                    {!hasLogged ? (
                      costBreakdown && (
                        <div className="space-y-1">
                          <div className="text-sm text-white/60">
                            Estimated cost (based on {task.estimated_minutes} min):
                          </div>
                          <CostLine cost={costBreakdown} />
                        </div>
                      )
                    ) : task?.status === 'completed' ? (
                      // Reached only when the settlement card could not render
                      // (no `cost` in the payload — an older backend, or a
                      // worklogs read that 403'd). The one-line total is the
                      // honest fallback there.
                      <div className="text-sm">
                        Final cost: <b>{formatCents(work.cost?.total_cents ?? work.total_cost_cents)}</b>
                      </div>
                    ) : (
                      <div className="space-y-1">
                        <div className="text-sm">
                          Logged: <b>{totalDuration}</b>
                        </div>
                        {costBreakdown && <CostLine cost={costBreakdown} />}
                        {/* The sessions behind that total, once there is more
                            than one of them. The single-session task reads the
                            same as it always did. */}
                        {timeline.filter(e => e.kind === 'session').length > 1 && (
                          <div className="pt-1 space-y-1">
                            <SessionList timeline={timeline} />
                            {gapsNote(timeline) && (
                              <div className="text-xs text-white/40">{gapsNote(timeline)}</div>
                            )}
                            {/* Why the rows can add up to more than the total
                                above them: the server bills CLOSED sessions
                                only, so the one still running is genuinely not
                                in that figure yet. */}
                            {work.has_open && (
                              <div className="text-xs text-white/40">
                                The session running now is added when it ends.
                              </div>
                            )}
                          </div>
                        )}
                      </div>
                    )}
                    {task?.assigned_to_id && task?.status === 'open' && (
                      <div className="text-xs text-white/40 mt-1 space-y-0.5">
                        {/* The same line the supporter sees, so both sides are
                            reading one set of numbers rather than two. */}
                        {timeBasisNote(capState?.cap) && <div>{timeBasisNote(capState.cap)}</div>}
                        <div>
                          Final cost is based on actual time logged. The base fee covers the first 15 minutes; time beyond that is charged per minute, and time nobody worked is never charged.
                        </div>
                      </div>
                    )}
                  </div>
                </div>

                {/* "On my way", the step before the clock. Solid, and ABOVE
                    clock-in, because it is the first thing a supporter does
                    after accepting — and because nothing is shared with the
                    requester until it is pressed. That is the privacy default:
                    opt in, once, deliberately. */}
                {canGoEnroute && (
                  <button
                    type="button"
                    onClick={goEnroute}
                    disabled={enrouteBusy}
                    className="w-full rounded-lg bg-white text-black font-medium px-4 py-3 text-sm hover:bg-white/90 disabled:opacity-60"
                  >
                    {enrouteBusy ? 'Starting…' : 'On my way'}
                  </button>
                )}
                {isEnrouteSharing && (
                  <div className="rounded-lg border border-white/15 bg-white/5 px-4 py-3 text-xs text-white/60">
                    Sharing your location with the requester until you clock out.
                  </div>
                )}

                {/* Clock in/out, full width and below the numbers it belongs
                    to. It used to be a 12px inline button wedged beside the
                    cost text — the single most-pressed control on the screen,
                    and the smallest thing on it, reached one-handed mid-errand.
                    Clocking OUT is styled solid because forgetting to is the
                    expensive mistake: it keeps billing somebody. */}
                {isAssignee && task.status === 'open' && (
                  work.has_open ? (
                    <>
                      {showWaitHint && (
                        <div className="text-xs text-white/50">{LAUNDRY_WAIT_HINT}</div>
                      )}
                      <button
                        type="button"
                        onClick={clockOut}
                        className="w-full rounded-lg bg-white text-black font-medium px-4 py-3 text-sm hover:bg-white/90"
                      >
                        Clock out
                      </button>
                    </>
                  ) : paused ? (
                    /* PAUSED — clocked out, task still live. This screen used
                       to offer a bare "Clock in" here and a separate "Mark as
                       Complete" far below it, with nothing saying what the
                       state between them was. Both exits now sit together,
                       under the line that answers the question a paused
                       supporter actually has: am I still being paid?

                       Complete is the solid one: a paused task that is
                       finished is the common case, and clocking back in is
                       the deliberate one. */
                    <>
                      <div className="rounded-lg border border-white/15 bg-white/5 px-4 py-3 text-sm text-white/70">
                        {PAUSED_SUPPORTER}
                      </div>
                      {showWaitHint && (
                        <div className="text-xs text-white/50">{LAUNDRY_WAIT_HINT}</div>
                      )}
                      <button
                        type="button"
                        onClick={() => { setCompletionPhotoURL(''); setCompletionNote(''); setShowCompleteModal(true) }}
                        style={{ background: '#9aab3a' }}
                        className="w-full rounded-lg px-4 py-3 text-sm font-medium text-white hover:opacity-90 transition-opacity"
                      >
                        Complete task
                      </button>
                      <button
                        type="button"
                        onClick={clockIn}
                        className="w-full rounded-lg border border-white/20 px-4 py-3 text-sm hover:border-white/40"
                      >
                        Clock back in
                      </button>
                    </>
                  ) : (
                    <>
                      {showWaitHint && (
                        <div className="text-xs text-white/50">{LAUNDRY_WAIT_HINT}</div>
                      )}
                      <button
                        type="button"
                        onClick={clockIn}
                        className="w-full rounded-lg border border-white/20 px-4 py-3 text-sm hover:border-white/40"
                      >
                        Clock in
                      </button>
                    </>
                  )
                )}

                {/* Assignee: live GPS while clocked in */}
                {isAssignee && work.has_open && (
                  <div className="mt-1">
                    {gpsError ? (
                      <span className="inline-flex items-center gap-1 rounded-full border border-red-400/30 bg-red-400/10 px-2 py-0.5 text-xs text-red-300">
                        ⚠ GPS: {gpsError}
                      </span>
                    ) : gpsPos ? (
                      <a
                        href={`https://www.google.com/maps?q=${gpsPos.lat},${gpsPos.lng}`}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex items-center gap-1 rounded-full border border-white/20 bg-white/5 px-2 py-0.5 text-xs hover:border-white/40"
                      >
                        📍 {gpsPos.lat.toFixed(5)}, {gpsPos.lng.toFixed(5)}
                        <span className="text-white/50">±{gpsPos.accuracy}m</span>
                      </a>
                    ) : (
                      <span className="inline-flex items-center gap-1 rounded-full border border-white/15 bg-white/5 px-2 py-0.5 text-xs text-white/60">
                        📡 Acquiring GPS…
                      </span>
                    )}
                  </div>
                )}
              </div>
            )}

            {task.assigned_to ? (
              <button
                type="button"
                onClick={() => setChatOpen(true)}
                className="w-full rounded-2xl border border-secondary/40 bg-surface/60 py-3.5 text-sm font-medium text-secondary hover:bg-surface hover:border-secondary/70 transition-all"
              >
                💬  Open Chat
              </button>
            ) : (
              <div className="w-full rounded-2xl border border-white/10 bg-surface/30 py-3.5 text-sm text-center text-white/30">
                💬 Chat available once a supporter accepts
              </div>
            )}

            {/* The disabled-until-you-have-clocked state. While PAUSED this is
                suppressed: the work-session card above carries an enabled
                "Complete task" beside "Clock back in", and showing a second,
                greyed-looking copy of the same action underneath it is how a
                supporter ends up pressing the wrong one. */}
            {isAssignee && !!task?.assigned_to_id && !paused && (
              <button
                type="button"
                disabled={!canComplete}
                onClick={() => { setCompletionPhotoURL(''); setCompletionNote(''); setShowCompleteModal(true) }}
                style={{ background: canComplete ? '#9aab3a' : undefined }}
                className={[
                  'w-full rounded-xl py-[14px] text-sm font-medium text-white transition-opacity',
                  canComplete ? 'opacity-100' : 'opacity-40 bg-white/10 cursor-not-allowed',
                ].join(' ')}
                title={canComplete ? undefined : 'Clock in & out at least once before completing'}
              >
                Mark as Complete
              </button>
            )}

            {/* Post-task feedback. The requester's review reached this page by
                email link only; the supporter had no entry point at all before
                the round, which is what canGiveFeedback adds. */}
            {(canReview || canGiveFeedback) && (
              <button
                type="button"
                onClick={() => navigate(`/tasks/${id}/review`)}
                style={{ background: '#9aab3a' }}
                className="w-full rounded-xl py-[14px] text-sm font-medium text-white transition-opacity hover:opacity-90"
              >
                {canReview ? 'Leave a review' : 'Share your feedback'}
              </button>
            )}

          </>
        ) : (
          /* 編輯區（原本邏輯保留） */
          /* ... */
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              saveEdit()
            }}
          >
            <div className="flex items-center justify-between">
              <h2 className="text-xl font-semibold">Edit task</h2>
              <button
                type="button"
                onClick={() => setEditing(false)}
                className="text-xs px-2 py-1 border border-white/20 rounded-md hover:border-white/40"
              >
                Cancel
              </button>
            </div>

            <div className="space-y-2">
              {/* Title */}
              <label className="block text-sm text-white/80">
                Title
                <input
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  required
                />
              </label>

              {/* Description */}
              <label className="block text-sm text-white/80">
                Description
                <textarea
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm min-h-20"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </label>

              {/* Category */}
              <label className="block text-sm text-white/80">
                Category
                <select
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={category}
                  onChange={(e) => setCategory(e.target.value)}
                >
                  <option value="task">Task</option>
                  <option value="companion">Companion</option>
                </select>
              </label>

              {/* Locations with PlaceInput */}
              <div className="grid gap-1">
                <label className="block text-sm text-white/80">Location(s)</label>
                <div className="space-y-2">
                  {locations.map((loc, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <div className="flex-1">
                        <PlaceInput
                          value={locations[i]}
                          placeholder={
                            i === 0 ? 'Address or meeting point' : 'Add another point'
                          }
                          onChange={(val) => updateLocation(i, val)}
                        />
                        {loc?.lat && loc?.lng && (
                          <div className="mt-1 text-xs text-white/60">
                            ({loc.lat.toFixed(6)}, {loc.lng.toFixed(6)})
                          </div>
                        )}
                      </div>
                      {i === locations.length - 1 ? (
                        <button
                          type="button"
                          onClick={addLocation}
                          className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40"
                        >
                          ＋
                        </button>
                      ) : (
                        <button
                          type="button"
                          onClick={() => removeLocation(i)}
                          className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40"
                        >
                          ×
                        </button>
                      )}
                    </div>
                  ))}
                </div>
              </div>

              {/* Estimated minutes */}
              <label className="block text-sm text-white/80">
                Estimated minutes
                <input
                  type="number"
                  min={5}
                  step={5}
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={minutes}
                  onChange={(e) => setMinutes(Number(e.target.value) || 0)}
                />
              </label>

              {/* Advance */}
              <label className="block text-sm text-white/80">
                Advance ($)
                <input
                  type="number"
                  min={0}
                  step={1}
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={prepay}
                  onChange={(e) => setPrepay(e.target.value)}
                />
              </label>

              {/* When */}
              <div className="space-y-2">
                <div className="text-sm text-white/80">When</div>
                <div className="flex gap-3 text-sm">
                  <label className="inline-flex items-center gap-2">
                    <input
                      type="radio"
                      name="mode"
                      value="now"
                      checked={mode === 'now'}
                      onChange={() => setMode('now')}
                    />
                    <span>ASAP</span>
                  </label>
                  <label className="inline-flex items-center gap-2">
                    <input
                      type="radio"
                      name="mode"
                      value="schedule"
                      checked={mode === 'schedule'}
                      onChange={() => setMode('schedule')}
                    />
                    <span>Schedule</span>
                  </label>
                </div>

                {mode === 'schedule' && (
                  <div className="flex gap-3">
                    <input
                      type="date"
                      className="flex-1 rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                      value={date}
                      onChange={(e) => setDate(e.target.value)}
                    />
                    <input
                      type="time"
                      className="flex-1 rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                      value={timeStr}
                      onChange={(e) => setTimeStr(e.target.value)}
                    />
                  </div>
                )}
              </div>
            </div>

            <button
              type="submit"
              className="w-full mt-4 rounded-md bg-white text-black px-4 py-2 text-sm font-medium hover:bg-white/90 disabled:opacity-50"
            >
              Save changes
            </button>
          </form>
        )}
      </div>
    </div>

    {/* Complete Task modal */}
    {showCompleteModal && (
      <div className="fixed inset-0 z-[300] flex items-center justify-center bg-black/60 px-4">
        <div className="w-full max-w-sm rounded-xl bg-surface border border-white/10 p-6 flex flex-col gap-4">
          <h3 className="text-base font-semibold">Complete Task</h3>

          {/* Photo upload */}
          <div>
            <p className="text-xs text-white/60 mb-2">
              Completion photo <span className="text-red-400">*</span>
            </p>
            {completionPhotoURL ? (
              <div className="relative">
                <img
                  src={completionPhotoURL}
                  alt="Completion preview"
                  className="w-full rounded-lg object-cover max-h-48"
                />
                <button
                  type="button"
                  onClick={() => { setCompletionPhotoURL(''); if (completionPhotoInputRef.current) completionPhotoInputRef.current.value = '' }}
                  className="absolute top-1 right-1 bg-black/60 text-white rounded-full w-6 h-6 text-xs flex items-center justify-center hover:bg-black/80"
                >
                  ✕
                </button>
              </div>
            ) : (
              <label className={[
                'flex flex-col items-center justify-center gap-2 w-full h-32 rounded-lg',
                'border-2 border-dashed border-white/20 hover:border-white/40',
                'cursor-pointer transition text-white/50 text-sm',
                photoUploading ? 'opacity-60 pointer-events-none' : '',
              ].join(' ')}>
                {photoUploading ? 'Uploading…' : (
                  <>
                    <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
                    <span>Tap to upload photo</span>
                  </>
                )}
                <input
                  ref={completionPhotoInputRef}
                  type="file"
                  className="sr-only"
                  accept="image/png,image/jpeg,image/webp,image/gif,image/heic,image/heif"
                  onChange={e => uploadCompletionPhoto(e.target.files?.[0])}
                  disabled={photoUploading}
                />
              </label>
            )}
          </div>

          {/* Note */}
          <textarea
            className="w-full rounded-lg bg-white/5 border border-white/15 px-3 py-2 text-sm text-white placeholder-white/40 resize-none focus:outline-none focus:border-white/30"
            rows={3}
            placeholder="Any notes for the requester? (optional)"
            value={completionNote}
            onChange={e => setCompletionNote(e.target.value)}
          />

          {/* Shopping settlement. Only on a task with an approved budget —
              everywhere else there is nothing to account for, and the server
              wants no receipt field at all. Zero is a real answer here
              ("nothing was bought") and needs no photo. */}
          {needsReceipt(approvedBudgetCents) && (
            <div className="space-y-2 border-t border-white/10 pt-4">
              <p className="text-xs text-white/60">
                Receipt total <span className="text-red-400">*</span>
                <span className="text-white/40">
                  {' '}— budget {formatCents(approvedBudgetCents)}, reimbursed up to{' '}
                  {formatCents(approvedBudgetCents + toleranceCents)}
                </span>
              </p>
              <input
                className="w-full rounded-lg bg-white/5 border border-white/15 px-3 py-2 text-sm text-white placeholder-white/40 focus:outline-none focus:border-white/30"
                placeholder="0.00 (enter 0 if you didn't buy anything)"
                inputMode="decimal"
                value={receiptAmount}
                onChange={e => setReceiptAmount(e.target.value)}
              />
              {receiptCents > 0 && (
                receiptPhotoURL ? (
                  <div className="relative">
                    <img
                      src={receiptPhotoURL}
                      alt="Receipt preview"
                      className="w-full rounded-lg object-cover max-h-48"
                    />
                    <button
                      type="button"
                      onClick={() => { setReceiptPhotoURL(''); if (receiptPhotoInputRef.current) receiptPhotoInputRef.current.value = '' }}
                      className="absolute top-1 right-1 bg-black/60 text-white rounded-full w-6 h-6 text-xs flex items-center justify-center hover:bg-black/80"
                    >
                      ✕
                    </button>
                  </div>
                ) : (
                  <label className={[
                    'flex items-center justify-center w-full h-20 rounded-lg',
                    'border-2 border-dashed border-white/20 hover:border-white/40',
                    'cursor-pointer transition text-white/50 text-sm',
                    receiptUploading ? 'opacity-60 pointer-events-none' : '',
                  ].join(' ')}>
                    {receiptUploading ? 'Uploading…' : 'Add a photo of the receipt'}
                    <input
                      ref={receiptPhotoInputRef}
                      type="file"
                      className="sr-only"
                      accept="image/png,image/jpeg,image/webp,image/gif,image/heic,image/heif"
                      onChange={e => uploadCompletionPhoto(e.target.files?.[0], setReceiptPhotoURL, setReceiptUploading)}
                      disabled={receiptUploading}
                    />
                  </label>
                )
              )}
              {receiptCents > approvedBudgetCents + toleranceCents && (
                <p className="text-xs text-red-300">
                  That&apos;s over the approved budget. Ask for a budget increase before completing,
                  or correct the amount — the most you can claim is{' '}
                  {formatCents(approvedBudgetCents + toleranceCents)}.
                </p>
              )}
            </div>
          )}

          {/* Buttons */}
          <div className="flex gap-3 justify-end">
            <button
              type="button"
              onClick={() => setShowCompleteModal(false)}
              className="px-4 py-2 text-sm rounded-lg border border-white/20 hover:border-white/40 transition"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={markCompleted}
              disabled={
                !completionPhotoURL ||
                photoUploading ||
                receiptUploading ||
                (approvedBudgetCents > 0 &&
                  (String(receiptAmount).trim() === '' ||
                    receiptCents > approvedBudgetCents + toleranceCents ||
                    (receiptCents > 0 && !receiptPhotoURL)))
              }
              className="px-4 py-2 text-sm rounded-lg bg-white text-black font-medium disabled:opacity-40 disabled:cursor-not-allowed hover:bg-white/90 transition"
            >
              Confirm Complete
            </button>
          </div>
        </div>
      </div>
    )}

    {/* Chat overlay — rendered outside the backdrop-blur card so z-index is at root stacking context */}
    {chatOpen && (
      <div className="fixed inset-0 z-[200] flex flex-col bg-primary">
        <div className="flex items-center px-4 py-4 bg-surface border-b border-white/10 shrink-0">
          <button
            type="button"
            onClick={() => setChatOpen(false)}
            className="flex items-center gap-1.5 text-secondary text-sm font-medium hover:opacity-80 transition-opacity"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <polyline points="15 18 9 12 15 6" />
            </svg>
            Back
          </button>
          <p className="flex-1 text-center text-sm font-semibold text-accent truncate px-4">{task.title}</p>
        </div>
        <div className="flex-1 min-h-0">
          <TaskChatBox task={task} me={user} fullscreen />
        </div>
      </div>
    )}
    </>
  )
}