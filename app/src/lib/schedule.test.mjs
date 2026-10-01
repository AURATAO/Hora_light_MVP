import test from 'node:test'
import assert from 'node:assert/strict'
import { defaultSchedule, localDateStr, localTimeStr, roundUpToStep, scheduleFields } from './schedule.js'

test('the date is the local one, not the UTC one', () => {
  // 21:30 local on Oct 1. In any zone west of UTC-2:30 the UTC date is Oct 2.
  const d = new Date(2026, 9, 1, 21, 30)
  assert.equal(localDateStr(d), '2026-10-01')
  assert.equal(localTimeStr(d), '21:30')
})

test('rounds up to the 5-minute step and leaves boundaries alone', () => {
  assert.equal(localTimeStr(roundUpToStep(new Date(2026, 9, 1, 21, 31, 10))), '21:35')
  assert.equal(localTimeStr(roundUpToStep(new Date(2026, 9, 1, 21, 35, 0))), '21:35')
  assert.equal(localTimeStr(roundUpToStep(new Date(2026, 9, 1, 21, 35, 1))), '21:40')
})

test('any hour is representable — the evening and overnight ones included', () => {
  assert.deepEqual(scheduleFields(new Date(2026, 9, 1, 22, 0)), { date: '2026-10-01', time: '22:00' })
  assert.deepEqual(scheduleFields(new Date(2026, 9, 2, 3, 15)), { date: '2026-10-02', time: '03:15' })
})

test('the default is an hour out, and rolls the date over midnight', () => {
  assert.deepEqual(defaultSchedule(new Date(2026, 9, 1, 14, 2)), { date: '2026-10-01', time: '15:05' })
  assert.deepEqual(defaultSchedule(new Date(2026, 9, 1, 23, 58)), { date: '2026-10-02', time: '01:00' })
})
