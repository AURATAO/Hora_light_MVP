// AvatarUploader.jsx
import { useState } from 'react'

export default function AvatarUploader({ value, onChange, size = 96, className = '' }) {
  const [uploading, setUploading] = useState(false)

  const isHeicLike = (file) => {
    const t = (file.type || '').toLowerCase()
    const n = (file.name || '').toLowerCase()
    return t.includes('heic') || t.includes('heif') || n.endsWith('.heic') || n.endsWith('.heif')
  }

  async function convertToJPEG(file, quality = 0.9) {
    const blob = file instanceof Blob ? file : new Blob([file])
    try {
      const bmp = await createImageBitmap(blob)
      const cvs = document.createElement('canvas')
      cvs.width = bmp.width
      cvs.height = bmp.height
      const ctx = cvs.getContext('2d')
      ctx.drawImage(bmp, 0, 0)
      const out = await new Promise((res, rej) =>
        cvs.toBlob(b => (b ? res(b) : rej(new Error('toBlob failed'))), 'image/jpeg', quality)
      )
      bmp.close?.()
      return out
    } catch {
      const url = URL.createObjectURL(blob)
      const img = await new Promise((resolve, reject) => {
        const i = new Image()
        i.onload = () => resolve(i)
        i.onerror = reject
        i.src = url
      })
      const cvs = document.createElement('canvas')
      cvs.width = img.naturalWidth || img.width
      cvs.height = img.naturalHeight || img.height
      const ctx = cvs.getContext('2d')
      ctx.drawImage(img, 0, 0)
      const out = await new Promise((res, rej) =>
        cvs.toBlob(b => (b ? res(b) : rej(new Error('toBlob failed'))), 'image/jpeg', quality)
      )
      URL.revokeObjectURL(url)
      return out
    }
  }

  async function handlePick(e) {
    const file = e.target.files?.[0]
    if (!file) return
    if (file.size > 5 * 1024 * 1024) {
      alert('Max 5MB')
      e.target.value = ''
      return
    }

    setUploading(true)
    try {
      let blob = file
      let mime = file.type || 'application/octet-stream'
      let name = file.name || 'avatar'

      if (isHeicLike(file)) {
        const jpeg = await convertToJPEG(file)
        blob = jpeg
        mime = 'image/jpeg'
        name = name.replace(/\.[^.]+$/, '') + '.jpg'
      } else if (!/^image\/(png|jpe?g|webp|gif)$/i.test(mime)) {
        alert('Only PNG/JPG/WebP/GIF')
        e.target.value = ''
        return
      }

      const fd = new FormData()
      fd.append('file', new File([blob], name, { type: mime }))

      const API_BASE =
        import.meta?.env?.VITE_API_BASE?.replace(/\/+$/, '') ||
        (location.hostname === 'localhost' ? 'http://localhost:8080' : 'https://api.horaapp.co')

      const res = await fetch(`${API_BASE}/profile/avatar`, {
        method: 'POST',
        credentials: 'include',
        body: fd,
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        console.error('[avatar][upload][fail]', res.status, data)
        throw new Error(data?.error || `Upload failed (${res.status})`)
      }

      const url = data.url
      const busted = url + (url.includes('?') ? '&' : '?') + 't=' + Date.now()
      onChange?.(busted)
    } catch (err) {
      console.error('[avatar][upload]', err)
      alert(err.message || 'Upload failed')
    } finally {
      setUploading(false)
      e.target.value = ''
    }
  }

  const s = `${size}px`

  return (
    <div className={`relative inline-block ${className}`} style={{ width: s, height: s }}>
      <img
        src={value || 'https://placehold.co/96x96?text=Avatar'}
        alt="avatar"
        className="rounded-full object-cover border border-white/20"
        style={{ width: s, height: s }}
        onError={(ev) => {
          const src = ev.currentTarget.src
          const clean = src.replace(/([?&])t=\d+(&|$)/, '$1').replace(/[?&]$/, '')
          if (src !== clean) ev.currentTarget.src = clean
        }}
      />

      {/* 橢圓 Upload pill：右下、略超出圓邊，tech vibe（玻璃+細邊） */}
      <label
        className={[
          'absolute bottom-0 right-0 translate-x-1/4 translate-y-1/4',
          'px-3 py-1 rounded-full',
          'text-[11px] tracking-wide uppercase',
          'backdrop-blur-sm',
          'bg-white/8 text-white',
          'border border-white/20',
          'shadow-sm hover:bg-white/12 active:scale-95 transition',
          'cursor-pointer select-none',
        ].join(' ')}
        title={uploading ? 'Uploading…' : 'Upload'}
      >
        {uploading ? 'Uploading…' : 'Upload'}
        <input
          type="file"
          className="sr-only"
          accept="image/png,image/jpeg,image/webp,image/gif,image/heic,image/heif"
          onChange={handlePick}
          disabled={uploading}
        />
      </label>
    </div>
  )
}