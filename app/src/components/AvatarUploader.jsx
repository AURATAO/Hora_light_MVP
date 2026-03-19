import { useRef, useState, useEffect } from 'react'
import { useToast } from '../providers/ToastProvider'

export default function AvatarUploader({ value, onChange, size = 96, className = '' }) {
  const [uploading, setUploading] = useState(false)
  const [displayURL, setDisplayURL] = useState(value || '')
  const inputRef = useRef(null)
  const lastBlobURL = useRef(null)
  const toast = useToast()

  useEffect(() => {
    // 父層 value 變動時，也更新顯示（加 bust）
    if (value) setDisplayURL(bust(value))
  }, [value])

  const isHeicLike = (file) => {
    const t = (file.type || '').toLowerCase()
    const n = (file.name || '').toLowerCase()
    return t.includes('heic') || t.includes('heif') || n.endsWith('.heic') || n.endsWith('.heif')
  }

  const bust = (url) => (url ? `${url}${url.includes('?') ? '&' : '?'}t=${Date.now()}` : url)

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
      toast('Max 5MB — please choose a smaller image', 'error')
      e.target.value = ''
      return
    }

    setUploading(true)

    // 先用本地預覽（樂觀 UI）
    const tempURL = URL.createObjectURL(file)
    if (lastBlobURL.current) URL.revokeObjectURL(lastBlobURL.current)
    lastBlobURL.current = tempURL
    setDisplayURL(tempURL)

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
        throw new Error('Only PNG/JPG/WebP/GIF')
      }

      const fd = new FormData()
      fd.append('file', new File([blob], name, { type: mime }))

      const API_BASE =
        import.meta?.env?.VITE_API_BASE?.replace(/\/+$/, '') ||
        (location.hostname === 'localhost' ? 'http://localhost:8080' : 'https://core.horaapp.co')

      const res = await fetch(`${API_BASE}/profile/avatar`, {
        method: 'POST',
        credentials: 'include',
        body: fd,
      })

      const data = await safeJSON(res)
      if (!res.ok) {
        throw new Error(data?.error || `Upload failed (${res.status})`)
      }

      const raw = data?.url
      if (!raw) throw new Error('Empty URL from server')

      // 通知父層存 raw URL（DB 乾淨資料）
      onChange?.(raw)

      // 我們自己顯示 bust 過的，確保立刻換圖
      setDisplayURL(bust(raw))
    } catch (err) {
      console.error('[avatar][upload]', err)
      toast(err.message || 'Upload failed')
      // 發生錯誤時，把預覽復原成舊 value
      setDisplayURL(value ? bust(value) : '')
    } finally {
      setUploading(false)
      if (inputRef.current) inputRef.current.value = ''
      // 釋放暫存 blob
      if (lastBlobURL.current) {
        URL.revokeObjectURL(lastBlobURL.current)
        lastBlobURL.current = null
      }
    }
  }

  const s = `${size}px`
  const fallback = `https://placehold.co/${size}x${size}?text=Avatar`
  const src = displayURL || value || fallback

  return (
    <div className={`relative inline-block ${className}`} style={{ width: s, height: s }}>
      <img
        key={src} // src 一變就強制重掛，避免舊圖殘留
        src={src}
        alt="avatar"
        className="rounded-full object-cover border border-white/20"
        style={{ width: s, height: s }}
        crossOrigin="anonymous"
        loading="eager"
        decoding="sync"
        onError={(ev) => {
          // 若 bust 的參數造成 404，移除 ?t=xxx 再試一次
          const bad = ev.currentTarget.src
          const clean = bad.replace(/([?&])t=\d+(&|$)/, '$1').replace(/[?&]$/, '')
          if (clean && clean !== bad) ev.currentTarget.src = clean
        }}
      />

      {/* 右下角 tech vibe 膠囊按鈕（文字版） */}
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
          ref={inputRef}
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

async function safeJSON(res) {
  try { return await res.json() } catch { return null }
}