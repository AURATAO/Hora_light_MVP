import { createContext, useCallback, useContext, useState } from 'react'

const ToastContext = createContext(null)

export function useToast() {
  return useContext(ToastContext)
}

let _id = 0

export function ToastProvider({ children }) {
  const [toasts, setToasts] = useState([])

  const dismiss = useCallback((id) => {
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  const toast = useCallback((message, type = 'error', duration = 4000) => {
    const id = ++_id
    setToasts(prev => [...prev, { id, message, type }])
    setTimeout(() => dismiss(id), duration)
  }, [dismiss])

  return (
    <ToastContext.Provider value={toast}>
      {children}
      <div
        className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 pointer-events-none"
        style={{ maxWidth: '360px', width: 'calc(100% - 32px)' }}
      >
        {toasts.map(t => (
          <ToastItem key={t.id} toast={t} onDismiss={() => dismiss(t.id)} />
        ))}
      </div>
    </ToastContext.Provider>
  )
}

const TYPE_STYLES = {
  error:   'bg-red-950/95 border-red-500/40 text-red-100',
  success: 'bg-green-950/95 border-green-500/40 text-green-100',
  info:    'bg-slate-800/95 border-white/20 text-white',
}

function ToastItem({ toast, onDismiss }) {
  const cls = TYPE_STYLES[toast.type] || TYPE_STYLES.info
  return (
    <div
      className={`flex items-start gap-3 rounded-lg border backdrop-blur-sm px-4 py-3 shadow-xl pointer-events-auto ${cls}`}
    >
      <span className="flex-1 text-sm leading-snug">{toast.message}</span>
      <button
        onClick={onDismiss}
        className="shrink-0 opacity-60 hover:opacity-100 text-lg leading-none mt-0.5 transition-opacity"
        aria-label="Dismiss notification"
      >
        ×
      </button>
    </div>
  )
}
