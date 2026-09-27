// Web mirror of mobile/test/safety-copy.test.mjs: the fallback report reasons
// must be a subset of the server's closed set (server/safety.go), label for
// label, or a report sent from the fallback is refused as invalid_reason.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { FALLBACK_REPORT_REASONS, blockConfirmTitle, safetyTargetName } from './safety.js'

const server = readFileSync(fileURLToPath(new URL('../../../server/safety.go', import.meta.url)), 'utf8')
const serverReasons = [...server.matchAll(/\{Value: "([a-z_]+)", Label: "([^"]+)"\}/g)].map((m) => ({ value: m[1], label: m[2] }))

test('every web fallback reason is a server reason with the same label', () => {
  assert.ok(serverReasons.length >= 5)
  for (const r of FALLBACK_REPORT_REASONS) {
    const s = serverReasons.find((x) => x.value === r.value)
    assert.ok(s, `${r.value} is not in server/safety.go`)
    assert.equal(r.label, s.label)
  }
})

test('names degrade to a neutral phrase', () => {
  assert.equal(safetyTargetName(''), 'this person')
  assert.equal(blockConfirmTitle('Maria'), 'Block Maria?')
})
