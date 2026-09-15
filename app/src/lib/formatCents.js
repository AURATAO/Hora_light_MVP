/**
 * Integer cents → "$12.34".
 *
 * A leaf module with no imports, so it can be pulled into a plain `node --test`
 * run without dragging React and the API client behind it — which is what
 * paymentCopy.test.mjs needs in order to pin the money wording.
 *
 * This is FORMATTING, not pricing. Every amount reaching it was computed in Go
 * (S-05); this is the one place a money value becomes a float, at the last step
 * before a string, mirroring formatCentsUSD in server/billing.go. There is no
 * arithmetic downstream of it.
 *
 * useTaskEstimate re-exports it so the dozens of existing `formatCents` call
 * sites stay exactly as they were.
 */
export function formatCents(cents) {
  return `$${((cents || 0) / 100).toFixed(2)}`
}
