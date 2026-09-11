import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * S-05 enforcement: pricing is computed in Go, and the web app displays what
 * the API returns without re-deriving any of it.
 *
 * This is a grep, not a unit test, and that is deliberate — the failure mode it
 * guards against is not "the arithmetic is wrong here" but "arithmetic exists
 * here at all". For two months three files each carried their own copy of the
 * fee schedule (`25 / 18 / 12`, `minutes * 0.50`), so the backend could not
 * change a fee without the web app quoting the old one to the person being
 * charged. Nothing caught it because every copy agreed with every other copy
 * at the moment it was written.
 *
 * If this test fails, the fix is to call POST /tasks/estimate (or read the
 * `cost` object from GET /tasks/:id/worklogs) — not to update the literal.
 */

const src = (path) => readFileSync(fileURLToPath(new URL('../' + path, import.meta.url)), 'utf8')

// Every file that renders a price to a user.
const PRICE_SURFACES = [
  'pages/NewTask.jsx',
  'pages/TaskDetail.jsx',
  'components/DurationPicker.jsx',
  'components/CancelTaskButton.jsx',
]

/** Strips comments so prose explaining the removed math doesn't trip the grep. */
function code(text) {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .filter((line) => !line.trim().startsWith('*') && !line.trim().startsWith('//'))
    .join('\n')
}

test('no web file re-derives a price from hardcoded constants', () => {
  for (const file of PRICE_SURFACES) {
    const body = code(src(file))

    // The per-minute rate, as arithmetic. The rate now arrives on every quote
    // as per_minute_rate_cents.
    assert.ok(
      !/[*]\s*0?\.50?\b/.test(body) && !/\b0\.50\s*[*]/.test(body),
      `${file} multiplies by the per-minute rate — use the server quote`
    )

    // The base-fee schedule, in cents or dollars.
    for (const literal of ['1200', '2500', '1800']) {
      assert.ok(
        !new RegExp(`\\b${literal}\\b`).test(body),
        `${file} contains the base-fee literal ${literal} — use base_fee_cents`
      )
    }

    // The dollar form of the old three-tier schedule, as a ternary or return.
    assert.ok(
      !/\?\s*25\s*:|\?\s*18\s*:|:\s*12\b/.test(body),
      `${file} looks like it branches on the old 25/18/12 base-fee tiers`
    )

    // `.toFixed(2)` on a money surface means a cents→dollars conversion that
    // bypassed formatCents, which is the one place that conversion belongs.
    assert.ok(
      !/toFixed\(2\)/.test(body),
      `${file} formats money by hand — use formatCents from hooks/useTaskEstimate`
    )
  }
})

test('the price surfaces get their numbers from the server', () => {
  // NewTask and TaskDetail must both be wired to a server quote. A file that
  // renders a price but imports no quote is exactly the regression above.
  for (const file of ['pages/NewTask.jsx', 'pages/TaskDetail.jsx']) {
    assert.match(
      src(file),
      /useTaskEstimate/,
      `${file} renders prices but never calls the quote endpoint`
    )
  }

  const hook = src('hooks/useTaskEstimate.js')
  assert.match(hook, /'\/tasks\/estimate'/, 'the quote hook must call POST /tasks/estimate')
})

test('the duration presets match mobile', () => {
  // Web offered 15/30/60/120 and mobile 30/60/90/120, so the same product
  // suggested different durations depending on the device. 15 is gone on both:
  // the first 15 minutes are inside the base fee, so it is the floor rather
  // than a cheaper package.
  const web = src('components/DurationPicker.jsx')
  assert.match(web, /const PRESETS = \[30, 60, 90, 120\]/, 'web presets drifted')

  let mobile
  try {
    mobile = readFileSync(
      fileURLToPath(new URL('../../../mobile/src/components/TaskForm.tsx', import.meta.url)),
      'utf8'
    )
  } catch {
    return // mobile/ not present in this checkout
  }
  assert.match(mobile, /QUICK_MINUTES = \[30, 60, 90, 120\]/, 'mobile presets drifted from web')
})

test('picking a duration no longer rewrites the category', () => {
  // The presets used to set the category as a side effect (15 → quick_errand,
  // else → standard), silently overwriting the user's choice — and since
  // category decides the base fee, a duration click could change the price of
  // a companionship task.
  const picker = code(src('components/DurationPicker.jsx'))
  assert.ok(
    !/quick_errand|'standard'|"standard"/.test(picker),
    'DurationPicker still couples duration to category'
  )
  assert.ok(
    !/onChange\([^)]*,[^)]*\)/.test(picker),
    'DurationPicker still passes a second (category) argument to onChange'
  )
})
