import { useEffect, useRef } from 'react'

declare global {
  interface Window {
    turnstile?: {
      render: (element: HTMLElement, options: Record<string, unknown>) => string
      remove?: (id: string) => void
    }
    __omniTurnstileReady?: Promise<void>
  }
}

function loadTurnstile(): Promise<void> {
  if (window.turnstile) return Promise.resolve()
  if (window.__omniTurnstileReady) return window.__omniTurnstileReady
  window.__omniTurnstileReady = new Promise<void>((resolve, reject) => {
    const existing = document.getElementById('cf-turnstile') as HTMLScriptElement | null
    const script = existing || document.createElement('script')
    const loaded = () => {
      cleanup()
      if (window.turnstile) resolve()
      else failed()
    }
    const failed = () => {
      cleanup()
      script.remove()
      window.__omniTurnstileReady = undefined
      reject(new Error('无法加载人机验证，请重试。'))
    }
    const cleanup = () => {
      script.removeEventListener('load', loaded)
      script.removeEventListener('error', failed)
    }
    script.addEventListener('load', loaded)
    script.addEventListener('error', failed)
    if (!existing) {
      script.id = 'cf-turnstile'
      script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
      script.async = true
      script.defer = true
      document.head.appendChild(script)
    }
  })
  return window.__omniTurnstileReady
}

interface TurnstileProps {
  siteKey: string
  onVerify: (token: string) => void
  onExpire?: () => void
  className?: string
}

export function Turnstile({ siteKey, onVerify, onExpire, className }: TurnstileProps) {
  const element = useRef<HTMLDivElement | null>(null)
  const callbacks = useRef({ onVerify, onExpire })
  callbacks.current = { onVerify, onExpire }
  useEffect(() => {
    let disposed = false
    let widget: string | undefined
    let consumed = false
    const showError = () => {
      if (disposed || !element.current) return
      element.current.setAttribute('role', 'alert')
      element.current.textContent = '验证暂不可用，请关闭验证窗口后重试。'
    }
    loadTurnstile().then(() => {
      if (disposed || !element.current || !window.turnstile) return
      widget = window.turnstile.render(element.current, {
        sitekey: siteKey,
        action: 'omni_auth',
        callback: (token: string) => {
          if (disposed || consumed) return
          consumed = true
          callbacks.current.onVerify(token)
        },
        'error-callback': showError,
        'expired-callback': () => { if (!disposed) callbacks.current.onExpire?.() },
      })
    }).catch(showError)
    return () => {
      disposed = true
      if (widget !== undefined) { try { window.turnstile?.remove?.(widget) } catch { /* Already removed by the vendor. */ } }
    }
  }, [siteKey])
  return <div ref={element} className={className} />
}
