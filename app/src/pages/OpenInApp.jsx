import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

/**
 * OpenInApp — the destination of every task link in a notification email.
 *
 * Emails used to link straight to /tasks/:id. Recipients read them on the phone
 * that has the TestFlight app installed, so tapping one dropped them into a web
 * session they were never signed in to. Linking to hora://task/:id instead
 * would fix that for app users and break it for everyone else: a custom scheme
 * is inert on a device without the app, and mail clients strip non-http hrefs.
 *
 * So this page sits in between. It is an ordinary https URL that every mail
 * client renders, and it lets the *device* decide: try the scheme, and offer
 * the web version to whoever is still here a moment later. No user-agent
 * sniffing — the thing being detected is whether hora:// resolves, which the
 * user agent string does not tell you anyway.
 *
 * Deliberately outside ProtectedLayout. Being signed out is the problem this
 * page exists to solve, so it must render for a signed-out reader.
 *
 * Known rough edge: on iOS Safari, a device *without* the app shows a "cannot
 * open the page" alert before this page's fallback becomes visible. Universal
 * Links are the fix and need an associated-domains entitlement plus an
 * apple-app-site-association file, neither of which exists yet; when they do,
 * this page stops being on the path at all. Until then the alert is dismissed
 * with one tap and the fallback is underneath it.
 */

// How long to wait before showing the fallback. Long enough that a device which
// is switching to the app doesn't flash this UI first, short enough not to feel
// broken to someone who has no app and is waiting for something to happen.
const FALLBACK_DELAY_MS = 1200

export default function OpenInApp() {
  const { id } = useParams()
  // Set by the /open/task/:id/review route. The app has no review screen, so
  // the deep link opens the task either way and only the web fallback keeps the
  // review path — which is the reader it matters to.
  const isReview = window.location.pathname.endsWith('/review')

  const [showFallback, setShowFallback] = useState(false)

  const appLink = id ? `hora://task/${encodeURIComponent(id)}` : null
  const webLink = id ? `/tasks/${encodeURIComponent(id)}${isReview ? '/review' : ''}` : '/'

  const openApp = useCallback(() => {
    if (!appLink) return
    // assign, not replace: the browser stays on this page so the fallback is
    // still here if the scheme goes nowhere, and Back from the app returns here
    // rather than to the mail client's blank tab.
    try {
      window.location.assign(appLink)
    } catch {
      // A browser that refuses the scheme outright — the fallback covers it.
    }
  }, [appLink])

  useEffect(() => {
    if (!id) {
      setShowFallback(true)
      return
    }
    openApp()
    const timer = setTimeout(() => setShowFallback(true), FALLBACK_DELAY_MS)
    return () => clearTimeout(timer)
  }, [id, openApp])

  return (
    <div className="min-h-screen bg-neutralbg px-4 py-16 text-slate-800">
      <div className="mx-auto flex max-w-sm flex-col items-center text-center">
        <img src="/Logo_icon.png" alt="HO:RA" className="mb-8 h-12 w-auto" />

        {!showFallback ? (
          <p className="text-sm text-slate-500">Opening the HO:RA app…</p>
        ) : !id ? (
          <>
            <h1 className="mb-2 text-lg font-semibold">That link is incomplete</h1>
            <p className="mb-6 text-sm text-slate-500">
              It's missing the task it should open. Try opening the app directly.
            </p>
            <Link
              to="/"
              className="w-full rounded-lg bg-brand px-4 py-3 text-sm font-medium text-white"
            >
              Go to HO:RA
            </Link>
          </>
        ) : (
          <>
            <h1 className="mb-2 text-lg font-semibold">Open your task</h1>
            <p className="mb-8 text-sm text-slate-500">
              If the HO:RA app didn't open on its own, tap below.
            </p>

            <button
              type="button"
              onClick={openApp}
              className="mb-3 w-full rounded-lg bg-brand px-4 py-3 text-sm font-medium text-white transition hover:opacity-90"
            >
              Open in the HO:RA app
            </button>

            <Link
              to={webLink}
              className="w-full rounded-lg border border-border px-4 py-3 text-sm font-medium text-slate-700 transition hover:bg-white"
            >
              Continue on web
            </Link>

            <p className="mt-8 text-xs text-slate-400">
              Don't have the app yet? Continue on web — everything works there too.
            </p>
          </>
        )}
      </div>
    </div>
  )
}
