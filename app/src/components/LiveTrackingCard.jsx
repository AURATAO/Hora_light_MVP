import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { loadMapsLib } from '../lib/googleLoader'
import {
  formatDistance,
  formatUpdatedAgo,
  liveStateDetail,
  liveStateLabel,
} from '../lib/liveTracking'

/**
 * The requester's live view of their supporter: one status card, one map, two
 * markers. The #1 ask out of Traction 3.
 *
 * What this component does NOT do is as deliberate as what it does. No route
 * polyline, no ETA, no animated marker gliding between fixes. A polyline is a
 * road we did not measure, an ETA is a promise we cannot keep, and
 * interpolation invents positions the supporter's phone never reported — all
 * three would be this screen claiming more than it knows about where a person
 * is. The markers jump on each poll, which is exactly as often as the truth
 * changes.
 *
 * Marker design is shared with mobile (mobile/src/components/LiveTrackingCard.tsx):
 * the supporter is their avatar in a brand-green ringed circle with a pointer
 * tail; the destination is an ink home pin. Tokens, not hexes, wherever the
 * platform has them.
 */

const POLL_INTERVAL_MS = 15_000
/** The ticker under the state label. Only the "12s ago" line needs it. */
const CLOCK_TICK_MS = 1_000

// design-tokens/colors.js. Web has no token import yet (mobile owns that
// module via Tailwind's Node require), so the two brand values this component
// needs are named here rather than spelled inline five times.
const BRAND = '#3A5A2D'
const GOLD = '#E1B145'
const INK = '#222831'

/** Milan, so an empty map is still a map. Overwritten by the first payload. */
const FALLBACK_CENTER = { lat: 45.4642, lng: 9.19 }

/**
 * The supporter's marker, as DOM.
 *
 * An avatar cannot go in a `google.maps.Marker` icon — an SVG data URI cannot
 * reliably pull in a remote image — so both markers are drawn as HTML through
 * OverlayView. That also means one set of styles for a pin that has to look
 * the same here and in react-native-maps.
 */
function supporterPinElement({ avatarUrl, stale }) {
  const el = document.createElement('div')
  el.style.cssText = 'position:absolute;transform:translate(-50%,-100%);pointer-events:none;'

  // A stale position is drawn in grey rather than hidden: the requester wants
  // to see where their supporter WAS, and the card above says plainly that it
  // is not current.
  const ring = stale ? '#8A8F87' : BRAND

  const pin = document.createElement('div')
  pin.style.cssText = `
    width:48px;height:48px;border-radius:9999px;
    border:3px solid ${ring};background:#fff;
    box-shadow:0 2px 8px rgba(0,0,0,.35);
    overflow:hidden;display:flex;align-items:center;justify-content:center;
    opacity:${stale ? '0.75' : '1'};
  `

  if (avatarUrl) {
    const img = document.createElement('img')
    img.src = avatarUrl
    img.alt = ''
    img.style.cssText = 'width:100%;height:100%;object-fit:cover;display:block;'
    // A broken avatar URL falls back to the logo dot rather than to a torn
    // image icon sitting on somebody's street.
    img.onerror = () => {
      img.remove()
      pin.appendChild(logoDot())
    }
    pin.appendChild(img)
  } else {
    pin.appendChild(logoDot())
  }

  // The tail. Drawn as a bordered triangle in the ring colour so the pin reads
  // as pointing AT a spot rather than sitting near one.
  const tail = document.createElement('div')
  tail.style.cssText = `
    width:0;height:0;margin:-2px auto 0;
    border-left:7px solid transparent;border-right:7px solid transparent;
    border-top:10px solid ${ring};
    filter:drop-shadow(0 2px 2px rgba(0,0,0,.25));
  `

  el.appendChild(pin)
  el.appendChild(tail)
  return el
}

/**
 * The HO:RA mark at pin scale: brand disc, gold colon. The colon is DRAWN, as
 * two dots, rather than typed — set in a system face at this size a ":" reads
 * as punctuation rather than as the logo, and two dots is what the wordmark
 * actually is. Kept identical to mobile's LogoDot.
 */
function logoDot() {
  const dot = document.createElement('div')
  dot.style.cssText = `
    width:100%;height:100%;border-radius:9999px;background:${BRAND};
    display:flex;flex-direction:column;align-items:center;justify-content:center;gap:4px;
  `
  for (let i = 0; i < 2; i += 1) {
    const pip = document.createElement('span')
    pip.style.cssText = `width:5px;height:5px;border-radius:9999px;background:${GOLD};display:block;`
    dot.appendChild(pip)
  }
  return dot
}

/** Location A. A home glyph, not a second circle — the two pins must never be confused. */
function destinationPinElement() {
  const el = document.createElement('div')
  el.style.cssText = 'position:absolute;transform:translate(-50%,-100%);pointer-events:none;'
  el.innerHTML = `
    <svg width="30" height="38" viewBox="0 0 30 38" fill="none" xmlns="http://www.w3.org/2000/svg"
         style="filter:drop-shadow(0 2px 6px rgba(0,0,0,.35));display:block;">
      <path d="M15 37C15 37 28 23.5 28 14.5C28 7.04 22.18 1 15 1C7.82 1 2 7.04 2 14.5C2 23.5 15 37 15 37Z"
            fill="${INK}" stroke="#ffffff" stroke-width="2"/>
      <path d="M9.5 14.8L15 10.2L20.5 14.8V21C20.5 21.55 20.05 22 19.5 22H10.5C9.95 22 9.5 21.55 9.5 21V14.8Z"
            fill="#ffffff"/>
    </svg>`
  return el
}

/**
 * One OverlayView per marker. Created once, then only repositioned — recreating
 * the overlay on every poll would make the pins flicker at 15s intervals.
 */
function makeOverlay(maps, element) {
  const overlay = new maps.OverlayView()
  overlay.position = null
  overlay.onAdd = function onAdd() {
    this.getPanes().floatPane.appendChild(element)
  }
  overlay.draw = function draw() {
    const projection = this.getProjection()
    if (!projection || !this.position) {
      element.style.display = 'none'
      return
    }
    const point = projection.fromLatLngToDivPixel(this.position)
    if (!point) return
    element.style.display = ''
    element.style.left = `${point.x}px`
    element.style.top = `${point.y}px`
  }
  overlay.onRemove = function onRemove() {
    element.remove()
  }
  return overlay
}

/**
 * The map. Mounted only once there is something to draw, and deliberately
 * dumb: it takes two positions and puts two pins on them.
 */
function LiveMap({ supporter, destination, stale, avatarUrl }) {
  const hostRef = useRef(null)
  const mapRef = useRef(null)
  const supporterOverlayRef = useRef(null)
  const destinationOverlayRef = useRef(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let alive = true
    loadMapsLib()
      .then(() => {
        if (!alive || !hostRef.current || mapRef.current) return
        const maps = window.google.maps
        mapRef.current = new maps.Map(hostRef.current, {
          center: supporter || destination || FALLBACK_CENTER,
          zoom: 15,
          disableDefaultUI: true,
          zoomControl: true,
          gestureHandling: 'cooperative',
          clickableIcons: false,
        })
        destinationOverlayRef.current = makeOverlay(maps, destinationPinElement())
        destinationOverlayRef.current.setMap(mapRef.current)
      })
      .catch(() => {
        // No key, a blocked script, a quota wall. The status card above is the
        // product; the map is decoration, and decoration does not get to take
        // the screen down with it.
        if (alive) setFailed(true)
      })
    return () => {
      alive = false
    }
    // Mount-only: later position changes are handled by the effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The supporter's pin is rebuilt only when its APPEARANCE changes (a new
  // avatar, or crossing into/out of stale), never on a position change.
  useEffect(() => {
    const map = mapRef.current
    if (!map) return
    const maps = window.google?.maps
    if (!maps) return
    supporterOverlayRef.current?.setMap(null)
    supporterOverlayRef.current = makeOverlay(maps, supporterPinElement({ avatarUrl, stale }))
    supporterOverlayRef.current.setMap(map)
  }, [avatarUrl, stale, failed])

  useEffect(() => {
    const map = mapRef.current
    const maps = window.google?.maps
    if (!map || !maps) return

    if (destination && destinationOverlayRef.current) {
      destinationOverlayRef.current.position = new maps.LatLng(destination.lat, destination.lng)
      destinationOverlayRef.current.draw()
    }
    if (supporter && supporterOverlayRef.current) {
      supporterOverlayRef.current.position = new maps.LatLng(supporter.lat, supporter.lng)
      supporterOverlayRef.current.draw()
    }

    // Keep both in frame. Fitting bounds on every poll would fight the user's
    // own pan and zoom, so it only happens while the two are far enough apart
    // that one of them would otherwise be off-screen.
    if (supporter && destination) {
      const bounds = new maps.LatLngBounds()
      bounds.extend(new maps.LatLng(supporter.lat, supporter.lng))
      bounds.extend(new maps.LatLng(destination.lat, destination.lng))
      map.fitBounds(bounds, 64)
    } else if (supporter) {
      map.setCenter(supporter)
    } else if (destination) {
      map.setCenter(destination)
    }
  }, [supporter?.lat, supporter?.lng, destination?.lat, destination?.lng])

  if (failed) return null
  return (
    <div
      ref={hostRef}
      className="mt-3 h-52 w-full overflow-hidden rounded-lg border border-white/10 bg-white/5"
      aria-label="Map showing your supporter's position and the task address"
      role="img"
    />
  )
}

/**
 * The card. Polls GET /tasks/:id/live while it is on screen and the tab is in
 * front, and stops otherwise — a backgrounded tab polling somebody's live
 * position every fifteen seconds is battery the requester did not agree to
 * spend and a read they are not there to look at.
 */
export default function LiveTrackingCard({ taskId }) {
  const [live, setLive] = useState(null)
  const [now, setNow] = useState(() => Date.now())
  const liveRef = useRef(null)

  const poll = useCallback(async () => {
    try {
      const data = await api(`/tasks/${taskId}/live`)
      liveRef.current = data
      setLive(data)
    } catch {
      // A dropped poll leaves the last answer on screen. It carries its own
      // "updated 40s ago", which ages honestly whether or not the next poll
      // lands — so a network blip degrades into a visibly stale card rather
      // than an error the requester has to dismiss.
    }
  }, [taskId])

  useEffect(() => {
    let timer = null

    function start() {
      if (timer) return
      poll()
      timer = setInterval(poll, POLL_INTERVAL_MS)
    }
    function stop() {
      if (!timer) return
      clearInterval(timer)
      timer = null
    }
    function onVisibility() {
      if (document.visibilityState === 'visible') start()
      else stop()
    }

    onVisibility()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [poll])

  // Ages the "updated 12s ago" line between polls, so it counts up instead of
  // sitting frozen for fifteen seconds at a time.
  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), CLOCK_TICK_MS)
    return () => clearInterval(tick)
  }, [])

  if (!live) return null

  const state = live.state
  const stale = state === 'unavailable'
  const supporterName = live.supporter?.name || ''
  const distance = formatDistance(live.distance_m)
  const updated = formatUpdatedAgo(live.updated_at, now)
  const position = live.lat != null && live.lng != null ? { lat: live.lat, lng: live.lng } : null

  return (
    <div className="rounded-xl border border-white/10 bg-white/[0.04] p-4">
      <div className="mb-2 text-xs uppercase tracking-wider text-white/40">Live</div>

      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span
              className={`h-2 w-2 shrink-0 rounded-full ${stale ? 'bg-white/30' : 'animate-pulse bg-[#7FB069]'}`}
              aria-hidden="true"
            />
            <span className="truncate text-base font-medium">{liveStateLabel(state)}</span>
          </div>
          <p className="mt-1 text-sm text-white/60">{liveStateDetail(state, supporterName)}</p>
        </div>

        {/* The distance sits opposite the state so the two read as one line:
            what is happening, and how far away it is happening. Absent rather
            than zeroed when the server withheld it. */}
        {distance && (
          <div className="shrink-0 text-right text-sm font-medium text-white/80">{distance}</div>
        )}
      </div>

      {updated && <div className="mt-2 text-xs text-white/40">{updated}</div>}

      {(position || live.destination) && (
        <LiveMap
          supporter={position}
          destination={live.destination}
          stale={stale}
          avatarUrl={live.supporter?.avatar_url || ''}
        />
      )}
    </div>
  )
}
