import test from 'node:test'
import assert from 'node:assert/strict'
import { apiErrorCode, apiErrorMessage } from './apiError.js'

test("the server's sentence wins", () => {
  assert.equal(
    apiErrorMessage(403, { error: 'outstanding_balance', message: 'You have an outstanding balance of $12.00 from "Coffee run". Settle it to keep posting.' }),
    'You have an outstanding balance of $12.00 from "Coffee run". Settle it to keep posting.',
  )
})

test('a phrase is shown, capitalised; a bare code never is', () => {
  assert.equal(apiErrorMessage(400, { error: 'already clocked in' }), 'Already clocked in')
  assert.equal(apiErrorMessage(403, { error: 'payouts_onboarding_required' }), "That didn't work (HTTP 403). Please try again.")
  assert.equal(apiErrorMessage(403, { error: 'forbidden' }), "That didn't work (HTTP 403). Please try again.")
})

test('a 5xx is generic unless the server wrote a sentence', () => {
  assert.equal(apiErrorMessage(500, { error: 'db error' }), 'Something went wrong on our side. Please try again.')
  assert.equal(apiErrorMessage(503, { error: 'unavailable', message: 'Card payments are temporarily unavailable.' }), 'Card payments are temporarily unavailable.')
})

test('non-JSON and empty bodies fall back without throwing', () => {
  assert.equal(apiErrorMessage(502, '<html>Bad Gateway</html>'), 'Something went wrong on our side. Please try again.')
  assert.equal(apiErrorMessage(404, ''), "That didn't work (HTTP 404). Please try again.")
  assert.equal(apiErrorMessage(400, null), "That didn't work (HTTP 400). Please try again.")
})

test('the code rides along for branching', () => {
  assert.equal(apiErrorCode({ error: 'not available' }), 'not available')
  assert.equal(apiErrorCode('<html>'), undefined)
})
