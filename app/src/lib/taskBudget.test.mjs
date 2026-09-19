import test from 'node:test'
import assert from 'node:assert/strict'
import { approvedBudgetCentsFor, needsReceipt } from './taskBudget.js'

// THE BUG THIS SUITE EXISTS FOR. A task posted while PAYMENTS_ENFORCED was on
// and completed after it went off still has a budget, and the server still
// refuses a completion that says nothing about the receipt. A client that had
// neither the extensions poll nor the worklogs settlement resolved the budget
// to 0, showed no receipt field, and left the supporter unable to complete the
// task from the app at all. The task's own field is the source that cannot be
// absent.

test('the task carries the decision when the side-channel fetches have not landed', () => {
  assert.equal(
    approvedBudgetCentsFor({ task: { shopping_budget_approved_cents: 2000 } }),
    2000,
  )
  assert.equal(
    approvedBudgetCentsFor({
      extensions: null,
      settlement: null,
      task: { shopping_budget_approved_cents: 2000 },
    }),
    2000,
  )
})

test('freshest source wins: an approved mid-task increase outranks the task row', () => {
  const budget = approvedBudgetCentsFor({
    extensions: { approved_budget_cents: 3500 },
    settlement: { approved_budget_cents: 2000 },
    task: { shopping_budget_approved_cents: 2000 },
  })
  assert.equal(budget, 3500, 'a raised ceiling must not be masked by a stale task row')
})

test('settlement outranks the task row when extensions is absent', () => {
  assert.equal(
    approvedBudgetCentsFor({
      settlement: { approved_budget_cents: 3500 },
      task: { shopping_budget_approved_cents: 2000 },
    }),
    3500,
  )
})

// Zero is a real answer — "this task has no shopping budget" — and must not be
// skipped over in favour of a later source, or a task whose budget was never
// set would inherit whatever a stale sibling field said.
test('an explicit zero from a fresher source is respected, not skipped', () => {
  assert.equal(
    approvedBudgetCentsFor({
      extensions: { approved_budget_cents: 0 },
      task: { shopping_budget_approved_cents: 2000 },
    }),
    0,
  )
})

test('nothing known at all is zero, not a crash', () => {
  assert.equal(approvedBudgetCentsFor(), 0)
  assert.equal(approvedBudgetCentsFor({}), 0)
  assert.equal(approvedBudgetCentsFor({ task: null }), 0)
  assert.equal(approvedBudgetCentsFor({ task: {} }), 0)
})

// Nullish, not falsy: a missing field must fall through to the next source,
// and the values that are NOT numbers must never be treated as a budget.
test('non-numeric junk falls through instead of becoming a budget', () => {
  assert.equal(
    approvedBudgetCentsFor({
      extensions: { approved_budget_cents: null },
      settlement: { approved_budget_cents: undefined },
      task: { shopping_budget_approved_cents: 2000 },
    }),
    2000,
  )
  assert.equal(
    approvedBudgetCentsFor({
      extensions: { approved_budget_cents: '3500' },
      task: { shopping_budget_approved_cents: 2000 },
    }),
    2000,
    'a string must not be read as a ceiling',
  )
  assert.equal(
    approvedBudgetCentsFor({
      extensions: { approved_budget_cents: NaN },
      task: { shopping_budget_approved_cents: 2000 },
    }),
    2000,
  )
})

test('a negative ceiling is clamped rather than hiding the receipt step', () => {
  assert.equal(approvedBudgetCentsFor({ task: { shopping_budget_approved_cents: -500 } }), 0)
})

test('needsReceipt matches the server rule: above zero, a receipt is owed', () => {
  assert.equal(needsReceipt(0), false)
  assert.equal(needsReceipt(1), true)
  assert.equal(needsReceipt(2000), true)
})

// The rule itself, stated as a test: nothing in this module may consult the
// live flag. It takes the task's state and nothing else.
test('the budget decision takes no flag argument and reads no global', () => {
  const src = approvedBudgetCentsFor.toString() + needsReceipt.toString()
  assert.ok(!/enforce/i.test(src), 'a payment-enforcement flag leaked into a per-task decision')
})
