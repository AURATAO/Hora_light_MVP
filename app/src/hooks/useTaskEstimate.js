import { useEffect, useState } from 'react'
import { api } from '../api/client'

/**
 * useTaskEstimate — the web app's only source of a price.
 *
 * Pricing is computed in Go and nowhere else (S-05). Before this hook, three
 * files each carried their own copy of the schedule — NewTask.jsx, TaskDetail.jsx
 * and DurationPicker.jsx all hardcoded `25 / 18 / 12` and `minutes * 0.50` —
 * so the backend could not change a fee without the web app quoting the old
 * one. Mobile never had the problem: it has always called POST /tasks/estimate.
 * This hook is web catching up.
 *
 * Debounced by 300ms and cancel-safe, matching mobile's TaskForm so the two
 * platforms behave identically while someone is typing a duration.
 *
 * Returns null until a quote is available. Callers render nothing rather than
 * a placeholder price: a stale or guessed number on a money surface is worse
 * than no number.
 *
 * @param {object}  args
 * @param {string}  args.category           task category, as it will be submitted
 * @param {number}  args.estimatedMinutes   duration to price
 * @param {number} [args.shoppingBudgetCents=0]
 * @param {boolean}[args.enabled=true]      skip the request entirely when false
 * @returns {{base_fee_cents:number, included_minutes:number, total_minutes:number,
 *            billable_minutes:number, time_cost_cents:number,
 *            shopping_budget_cents:number, total_cents:number} | null}
 */
export function useTaskEstimate({
  category,
  estimatedMinutes,
  shoppingBudgetCents = 0,
  enabled = true,
}) {
  const [estimate, setEstimate] = useState(null)

  useEffect(() => {
    const minutes = Number(estimatedMinutes)
    // Gated on category AND a positive duration, not category alone: on a form
    // where duration starts empty, defaulting it to 0 renders a degenerate
    // "$12.00 total, 0 min" card the moment a category is picked, which reads
    // as broken rather than as genuinely appearing.
    if (!enabled || !category || !Number.isFinite(minutes) || minutes <= 0) {
      setEstimate(null)
      return
    }

    let cancelled = false
    const timer = setTimeout(async () => {
      try {
        const result = await api('/tasks/estimate', {
          method: 'POST',
          body: {
            category,
            estimated_minutes: minutes,
            prepay_amount_cents: Math.max(0, Math.round(shoppingBudgetCents || 0)),
          },
        })
        if (!cancelled) setEstimate(result)
      } catch {
        // Includes the over-cap 400, where the form's own validation is
        // already telling the user what is wrong. Showing no price is the
        // correct outcome either way.
        if (!cancelled) setEstimate(null)
      }
    }, 300)

    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [category, estimatedMinutes, shoppingBudgetCents, enabled])

  return estimate
}

/** Renders integer cents. The one place the web app turns money into a string. */
export function formatCents(cents) {
  return `$${((cents || 0) / 100).toFixed(2)}`
}
