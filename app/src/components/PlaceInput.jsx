import { useEffect, useRef, useState } from 'react'
import { loadPlacesLib } from '../lib/googleLoader'

function newSessionToken() {
  return new google.maps.places.AutocompleteSessionToken()
}

export default function PlaceInput({ value, onChange, placeholder, countryCodes, locationBias }) {
  const [ready, setReady] = useState(false)
  const [query, setQuery] = useState(value?.label || '')
  const [preds, setPreds] = useState([])
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const boxRef = useRef(null)
  const sessionRef = useRef(null)

  useEffect(() => {
    let alive = true
    loadPlacesLib()
      .then(() => {
        if (!alive) return
        sessionRef.current = newSessionToken()
        setReady(true)
      })
      .catch((e) => console.error('[PlaceInput] loadPlacesLib failed:', e))
    return () => { alive = false }
  }, [])

  useEffect(() => { setQuery(value?.label || '') }, [value?.label])

  useEffect(() => {
    function onDoc(e) {
      if (!boxRef.current) return
      if (!boxRef.current.contains(e.target)) setOpen(false)
    }
    document.addEventListener('click', onDoc)
    return () => document.removeEventListener('click', onDoc)
  }, [])

  // 🔎 取「新版」自動完成建議
  useEffect(() => {
    if (!ready) return
    const text = query?.trim()
    if (!text) { setPreds([]); return }

    const id = setTimeout(async () => {
      setLoading(true)
      try {
        const req = { input: text, sessionToken: sessionRef.current }
        if (countryCodes?.length) req.componentRestrictions = { country: countryCodes }
        if (locationBias) req.locationBias = locationBias

        const { suggestions } =
          await google.maps.places.AutocompleteSuggestion.fetchAutocompleteSuggestions(req)

        setPreds(suggestions || [])
        setOpen(true)
      } catch (err) {
        console.error('[PlaceInput] suggestions failed', err)
        setPreds([])
      } finally {
        setLoading(false)
      }
    }, 150)

    return () => clearTimeout(id)
  }, [query, ready, countryCodes, locationBias])

  // 📍 選中建議 → 取 Place 詳細（取代舊的 PlacesService.getDetails）
  async function selectSuggestion(sug) {
    setOpen(false)
    setQuery(sug.placePrediction?.text?.text || query)
    setLoading(true)

    try {
      // 建議物件 → Place 實例
      const place = sug.placePrediction.toPlace()
      await place.fetchFields({ fields: ['id', 'displayName', 'formattedAddress', 'location'] })

      const label =
        place.formattedAddress ||
        place.displayName ||
        sug.placePrediction?.text?.text ||
        query

      const loc = place.location
      const lat = typeof loc?.lat === 'function' ? loc.lat() : loc?.lat
      const lng = typeof loc?.lng === 'function' ? loc.lng() : loc?.lng

      onChange?.({
        label,
        placeId: place.id || null,   // 新版用 id
        lat: (typeof lat === 'number' ? lat : undefined) ?? null,
        lng: (typeof lng === 'number' ? lng : undefined) ?? null,
      })

      sessionRef.current = newSessionToken()
    } catch (e) {
      console.error('[PlaceInput] fetchFields failed', e)
      onChange?.({ label: sug.placePrediction?.text?.text || query })
      sessionRef.current = newSessionToken()
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="relative" ref={boxRef}>
      <input
        className="w-full rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
        value={query}
        onChange={(e) => {
          const v = e.target.value
          setQuery(v)
          onChange?.(v.trim() ? { label: v } : null) // 仍支援手打純文字
        }}
        placeholder={placeholder || 'Search a place'}
        onFocus={() => preds.length && setOpen(true)}
        disabled={!ready}
      />

      {open && (preds.length > 0 || loading) && (
        <div className="absolute z-50 mt-1 w-full rounded-md border border-white/20 bg-primary/95 backdrop-blur-md shadow">
          {loading && <div className="px-3 py-2 text-sm text-white/70">Searching…</div>}
          {preds.map((sug, idx) => (
            <button
              key={idx}
              type="button"
              className="block w-full text-left px-3 py-2 text-sm hover:bg-white/10"
              onClick={() => selectSuggestion(sug)}
            >
              {sug.placePrediction?.text?.text || ''}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}