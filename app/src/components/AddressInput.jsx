import { useState } from 'react'
import PlaceInput from './PlaceInput'

// Bias autocomplete results toward New York City metro area
const NYC_BIAS = { center: { lat: 40.7128, lng: -74.006 }, radius: 40000 }

export default function AddressInput({ onChange }) {
  const [place, setPlace] = useState(null)
  const [apt, setApt] = useState('')

  function emit(p, aptVal) {
    if (!p?.label) {
      onChange?.({ label: '', placeId: null, lat: null, lng: null })
      return
    }
    // Insert apt after street (first segment), before city/state/zip
    let label = p.label
    if (aptVal?.trim()) {
      const commaIdx = label.indexOf(',')
      label = commaIdx !== -1
        ? `${label.slice(0, commaIdx)}, ${aptVal.trim()}${label.slice(commaIdx)}`
        : `${label}, ${aptVal.trim()}`
    }
    onChange?.({ ...p, label })
  }

  function handlePlaceChange(val) {
    const normalized = val?.label ? val : null
    setPlace(normalized)
    // Reset apt when address is cleared
    if (!normalized) setApt('')
    emit(normalized, apt)
  }

  function handleAptChange(e) {
    const val = e.target.value
    setApt(val)
    emit(place, val)
  }

  return (
    <div className="space-y-2">
      <PlaceInput
        value={place}
        placeholder="Street address"
        locationBias={NYC_BIAS}
        onChange={handlePlaceChange}
      />
      {place?.label && (
        <input
          className="w-full rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40 text-sm"
          value={apt}
          onChange={handleAptChange}
          placeholder="Apt / Suite / Floor (optional)"
        />
      )}
    </div>
  )
}
