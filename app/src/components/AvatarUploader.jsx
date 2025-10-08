import { useState } from 'react'
import { supabase } from '../lib/supabaseClient'

const BUCKET = 'avatars'

export default function AvatarUploader({ value, onChange, className = '' }) {
  const [uploading, setUploading] = useState(false)

  async function onFile(e) {
    const file = e.target.files?.[0]
    if (!file) return

    // 類型 & 大小限制（你可調整）
    if (!/^image\/(png|jpe?g|webp)$/i.test(file.type)) {
      alert('Only PNG/JPG/WEBP allowed')
      e.target.value = ''
      return
    }
    if (file.size > 2 * 1024 * 1024) {
      alert('Max 2MB')
      e.target.value = ''
      return
    }

    setUploading(true)
    try {
      const { data: { user }, error: authErr } = await supabase.auth.getUser()
      if (authErr) throw authErr
      if (!user?.id) throw new Error('Not signed in')

      // 副檔名更穩：先看檔名，沒有就從 MIME 推，最後預設 jpg
      let ext = (file.name.split('.').pop() || '').toLowerCase()
      if (!ext || ext.length > 5) {
        const mime = file.type.toLowerCase()
        if (mime.includes('png')) ext = 'png'
        else if (mime.includes('webp')) ext = 'webp'
        else ext = 'jpg'
      }
      const path = `${user.id}/${Date.now()}.${ext}`

      const { error: upErr } = await supabase.storage
        .from(BUCKET)
        .upload(path, file, {
          upsert: true,
          cacheControl: '3600',
          contentType: file.type || 'image/jpeg',
        })
      if (upErr) throw upErr

      // 公開 bucket：直接拿 public URL
      const { data } = supabase.storage.from(BUCKET).getPublicUrl(path)
      const url = data.publicUrl

      // 回傳給父層（父層決定要不要 PATCH /profile）
      onChange?.(url)
    } catch (err) {
      console.error('[avatar][upload]', err)
      alert(err.message || 'Upload failed')
    } finally {
      setUploading(false)
      e.target.value = '' // reset input
    }
  }

  return (
    <div className={`relative w-20 h-20 ${className}`}>
      <img
        src={value || 'https://placehold.co/80x80?text=Avatar'}
        alt="avatar"
        className="w-20 h-20 rounded-full object-cover border border-white/20"
      />
      <label className="absolute -bottom-1 -right-1 text-[11px] px-2 py-0.5 rounded-full bg-white text-black cursor-pointer shadow">
        {uploading ? 'Uploading…' : 'Upload'}
        <input
          type="file"
          accept="image/png,image/jpeg,image/webp"
          className="sr-only"
          onChange={onFile}
          disabled={uploading}
        />
      </label>
    </div>
  )
}