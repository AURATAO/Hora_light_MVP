import { useEffect } from 'react'

/**
 * 通用 Modal 元件
 *
 * Props:
 * - open: boolean                    // 是否開啟
 * - onClose: () => void              // 關閉時呼叫（背景、ESC、右上角）
 * - title?: string                   // 標題
 * - children?: ReactNode             // 內容（描述/自訂區塊）
 * - actions?: ReactNode              // 底部按鈕區塊
 * - autoCloseMs?: number             // 自動關閉毫秒數（可選）
 */
export default function Modal({ open, onClose, title, children, actions, autoCloseMs }) {
  useEffect(() => {
    if (!open) return
    function onKey(e) {
      if (e.key === 'Escape') onClose?.()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  useEffect(() => {
    if (!open || !autoCloseMs) return
    const id = setTimeout(() => onClose?.(), autoCloseMs)
    return () => clearTimeout(id)
  }, [open, autoCloseMs, onClose])

  if (!open) return null

  return (
    <div role="dialog" aria-modal="true" className="fixed inset-0 z-50 flex items-center justify-center">
      {/* 背景遮罩 */}
      <div className="absolute inset-0 bg-black/60" onClick={onClose} />
      {/* 內容卡片 */}
      <div className="relative z-10 w-[90%] max-w-sm rounded-2xl border border-white/20 bg-primary/90 backdrop-blur-md p-6 shadow-xl">
        <div className="flex items-start justify-between gap-4">
          {title ? <h3 className="text-lg font-semibold">{title}</h3> : <span />}
          <button
            className=" px-1 py-1 hover:border-white/40"
            onClick={onClose}
            aria-label="Close"
          >
            ×
          </button>
        </div>

        <div className="mt-2 text-sm text-white/80">
          {children}
        </div>

        {actions && (
          <div className="mt-5 flex justify-end gap-2">
            {actions}
          </div>
        )}
      </div>
    </div>
  )
}