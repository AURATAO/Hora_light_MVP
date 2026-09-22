import test from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * Nobody's name is made from their email address on a client.
 *
 * Build 11: "taoaura.lavoro is on the way" on a requester's lock screen. The
 * server seeded every profile name from the email's local part and both
 * clients ALSO carried their own copy of that derivation as a fallback, so the
 * same non-name was produced in three places and looked like a chosen one in
 * all of them. The server now resolves names in exactly one place
 * (server/names.go, helpers/names.go) and the clients print what they are
 * given. This is the grep that keeps it that way: no client source splits an
 * email at '@' to show a person.
 */

const here = fileURLToPath(new URL('.', import.meta.url))
const roots = [
  join(here, '..'),                              // app/src
  join(here, '..', '..', '..', 'mobile', 'src'), // mobile/src
]

function* walk(dir) {
  let entries
  try { entries = readdirSync(dir) } catch { return }
  for (const name of entries) {
    if (name === 'node_modules') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) yield* walk(p)
    else if (/\.(jsx?|tsx?)$/.test(name) && !/\.test\./.test(name)) yield p
  }
}

// The shapes both clients used: email.split('@')[0], email.indexOf('@') with a
// slice, and a function that existed only to do it.
const DERIVATIONS = [
  /split\(\s*['"]@['"]\s*\)/,
  /indexOf\(\s*['"]@['"]\s*\)/,
  /function\s+deriveName\b/,
]

test('no client source derives a display name from an email', () => {
  const offenders = []
  for (const root of roots) {
    for (const file of walk(root)) {
      const src = readFileSync(file, 'utf8')
      for (const re of DERIVATIONS) {
        if (re.test(src)) offenders.push(`${file.replace(here, 'app/src/')}: ${re}`)
      }
    }
  }
  assert.deepEqual(offenders, [], 'email-derived names found — the server owns name resolution')
})

test('the web client reads the name the server resolved', () => {
  const client = readFileSync(join(here, '..', 'api', 'client.js'), 'utf8')
  assert.match(client, /display_name_set/, '/auth/me display_name_set is what gates the name prompt')
  const prompt = readFileSync(join(here, '..', 'components', 'NamePrompt.jsx'), 'utf8')
  assert.match(prompt, /What should we call you\?/)
})
