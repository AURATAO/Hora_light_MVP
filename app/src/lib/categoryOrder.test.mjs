import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { POST_CATEGORY_ORDER, CATEGORY_META } from './categoryOrder.js'

/**
 * The category order, and the promise that it exists in exactly one place per
 * client and in exactly the same place on both.
 *
 * Build 11: the mobile home page and the mobile post form listed the
 * categories in two different orders, and the form carried one the home page
 * did not. Nobody noticed because each list was correct on its own terms. So
 * this is a grep as much as a test, in the pricing.test.mjs tradition: the
 * failure it guards against is not "the order is wrong" but "a second order
 * exists".
 */

const read = (rel) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8')

const SPEC_ORDER = ['quick_errand', 'delivery', 'laundry', 'grocery', 'queue', 'companionship']

test('the order is the one the home page shows', () => {
  assert.deepEqual([...POST_CATEGORY_ORDER], SPEC_ORDER)
  // Every listed category introduces itself; nothing extra is described.
  assert.deepEqual(Object.keys(CATEGORY_META).sort(), [...SPEC_ORDER].sort())
})

test('mobile carries the same order, byte for byte', (t) => {
  let src
  try {
    src = read('../../../mobile/src/lib/categories.ts')
  } catch {
    t.skip('mobile/ not present')
    return
  }
  const m = src.match(/POST_CATEGORY_ORDER[^=]*=\s*\[([^\]]*)\]/)
  assert.ok(m, 'mobile/src/lib/categories.ts no longer exports POST_CATEGORY_ORDER')
  const mobileOrder = [...m[1].matchAll(/"([a-z_]+)"/g)].map((x) => x[1])
  assert.deepEqual(mobileOrder, [...POST_CATEGORY_ORDER],
    'the two clients list the categories differently — the thing this constant exists to prevent')
})

test('no surface keeps a category list of its own', (t) => {
  // A literal array with two or more category slugs, outside the one file per
  // client that is allowed to have it.
  const listLiteral = /\[\s*['"](?:quick_errand|delivery|laundry|grocery|queue|companionship|companion|anything_else)['"]\s*,\s*['"](?:quick_errand|delivery|laundry|grocery|queue|companionship|companion|anything_else)['"]/
  for (const rel of ['../pages/CategoryHome.jsx', '../pages/NewTask.jsx']) {
    assert.ok(!listLiteral.test(read(rel)), `${rel} defines its own category order`)
  }
  let home, form
  try {
    home = read('../../../mobile/src/app/(tabs)/home.tsx')
    form = read('../../../mobile/src/components/TaskForm.tsx')
  } catch {
    t.skip('mobile/ not present')
    return
  }
  assert.ok(!listLiteral.test(home), 'mobile home.tsx defines its own category order')
  assert.ok(!listLiteral.test(form), 'mobile TaskForm.tsx defines its own category order')
})
