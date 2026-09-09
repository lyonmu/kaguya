import { it } from 'node:test'
import assert from 'node:assert/strict'
import { usageDateRange } from './range'

it('converts calendar dates to inclusive UTC Unix seconds, independent of local timezone', () => {
  const timezone = process.env.TZ
  try {
    for (const zone of ['UTC', 'Asia/Shanghai', 'America/New_York']) {
      process.env.TZ = zone
      assert.deepEqual(usageDateRange('2025-01-01', '2025-01-03'), [1735689600, 1735948799])
      const [start, end] = usageDateRange('2024-03-10', '2024-03-10')
      assert.equal(end - start, 86399) // 美国夏令时切换日也按 UTC 完整一天计算。
      assert.ok(Number.isInteger(start) && Number.isInteger(end))
      assert.equal(usageDateRange('2024-02-29', '2024-02-29')[1] + 1, usageDateRange('2024-03-01', '2024-03-01')[0])
    }
  } finally {
    if (timezone === undefined) delete process.env.TZ
    else process.env.TZ = timezone
  }
})
