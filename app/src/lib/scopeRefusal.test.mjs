// The web path of the scope rules (server/task_scope.go): the server's
// refusal reaches the person as its own sentence, on the post form and the
// AI box, and nothing else is read as one.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { scopeRefusal } from './scopeRefusal.js'
import { apiErrorMessage, apiErrorCode } from './apiError.js'

// What api() throws for a response body (src/api/client.js).
function thrown(status, body) {
  const err = new Error(apiErrorMessage(status, body))
  err.status = status
  err.code = apiErrorCode(body)
  err.body = body
  return err
}

const reply = "Sorry, HO:RA can't drive people, and supporters can't drive or move your car. A supporter can carry your bags to the car or the curb, pick something up, or bring it to you."

test('a 422 out_of_scope carries the category reply', () => {
  const r = scopeRefusal(thrown(422, { error: 'out_of_scope', category: 'driving', message: reply }))
  assert.deepEqual(r, { message: reply, category: 'driving', retry: false })
})

test('a 503 scope_unchecked asks to try again', () => {
  const r = scopeRefusal(thrown(503, { error: 'scope_unchecked', message: "We couldn't check this request just now." }))
  assert.equal(r.retry, true)
  assert.equal(r.message, "We couldn't check this request just now.")
})

test('other failures are not scope refusals', () => {
  assert.equal(scopeRefusal(thrown(400, { error: 'outside_service_area', message: 'Outside NYC' })), null)
  assert.equal(scopeRefusal(thrown(500, { error: 'db error' })), null)
  assert.equal(scopeRefusal(new Error('network')), null)
  assert.equal(scopeRefusal(undefined), null)
})

// The server's reply is the one in playbooks.json: the message a test
// expects is the one the server sends.
test('the server sends each category reply from playbooks.json', () => {
  const pb = JSON.parse(readFileSync(fileURLToPath(new URL('../../../server/internal/channel/llm/playbooks.json', import.meta.url)), 'utf8'))
  const driving = pb.scope_rules.refusals.find((r) => r.id === 'driving')
  assert.equal(driving.reply, reply)
  const go = readFileSync(fileURLToPath(new URL('../../../server/task_scope.go', import.meta.url)), 'utf8')
  assert.match(go, /"error": "out_of_scope", "category": d\.Refusal\.ID, "message": d\.Refusal\.Reply/)
})

// The post form and the AI box show it; the edit form toasts e.message,
// which is the server's sentence (apiErrorMessage).
test('NewTask and CategoryHome show the scope refusal', () => {
  const read = (p) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
  const newTask = read('../pages/NewTask.jsx')
  assert.match(newTask, /import \{ scopeRefusal \} from '\.\.\/lib\/scopeRefusal'/)
  assert.match(newTask, /const scope = scopeRefusal\(err\)/)
  assert.match(newTask, /\{scopeError && \(/)
  const home = read('../pages/CategoryHome.jsx')
  assert.match(home, /scopeRefusal\(err\)/)
  assert.equal(thrown(422, { error: 'out_of_scope', message: reply }).message, reply)
})
