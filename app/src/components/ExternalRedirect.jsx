import { useEffect } from 'react'

/**
 * A route that hands off to a page on another origin. `replace`, so Back does
 * not land on the redirect again. The link is rendered too: a blocked or slow
 * navigation still leaves something to click.
 */
export default function ExternalRedirect({ to, label }) {
  useEffect(() => {
    window.location.replace(to)
  }, [to])
  return (
    <div className="min-h-screen bg-primary text-accent p-6 text-sm">
      Taking you to the <a href={to} className="underline">{label}</a>…
    </div>
  )
}
