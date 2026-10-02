import { describe, expect, it } from 'vitest'

import {
  isCancelledScanError,
  runConcurrentScan
} from '../scanner'

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason?: unknown) => void
}

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function waitFor(condition: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 20 && !condition(); attempt += 1) {
    await Promise.resolve()
  }
  expect(condition()).toBe(true)
}

describe('account health cancellation detection', () => {
  it('recognizes explicit cancellation and gives an aborted signal precedence', () => {
    expect(isCancelledScanError({ code: 'ERR_CANCELED' })).toBe(true)
    expect(isCancelledScanError({ name: 'AbortError' })).toBe(true)

    const controller = new AbortController()
    controller.abort()
    expect(isCancelledScanError({ status: 401 }, controller.signal)).toBe(true)
  })

  it('does not mistake ordinary request failures for cancellation', () => {
    const controller = new AbortController()

    expect(isCancelledScanError(new Error('request failed'), controller.signal)).toBe(false)
    expect(isCancelledScanError({
      isAxiosError: true,
      code: 'ERR_BAD_RESPONSE',
      response: { status: 500 }
    }, controller.signal)).toBe(false)
    expect(isCancelledScanError({
      isAxiosError: true,
      code: 'ECONNABORTED',
      message: 'timeout of 30000ms exceeded'
    }, controller.signal)).toBe(false)
    expect(isCancelledScanError({ status: 401 }, controller.signal)).toBe(false)
  })
})

describe('runConcurrentScan', () => {
  it('normalizes zero and negative concurrency to one worker', async () => {
    for (const concurrency of [0, -3]) {
      const controller = new AbortController()
      const gate = createDeferred<void>()
      const started: number[] = []
      const scanPromise = runConcurrentScan([1, 2], {
        concurrency,
        signal: controller.signal,
        scan: async () => gate.promise,
        onStart: (item) => started.push(item)
      })

      expect(started).toEqual([1])
      gate.resolve()
      await scanPromise
      expect(started).toEqual([1, 2])
    }
  })

  it('never exceeds the configured concurrency and passes stable indexes and signal', async () => {
    const items = [10, 20, 30, 40]
    const controller = new AbortController()
    const gates = new Map(items.map((item) => [item, createDeferred<string>()]))
    const started: Array<[number, number]> = []
    const succeeded: Array<[number, string, number]> = []
    const seenSignals: AbortSignal[] = []
    let active = 0
    let peakActive = 0

    const scanPromise = runConcurrentScan(items, {
      concurrency: 2.9,
      signal: controller.signal,
      scan: async (item, signal) => {
        seenSignals.push(signal)
        active += 1
        peakActive = Math.max(peakActive, active)
        try {
          return await gates.get(item)!.promise
        } finally {
          active -= 1
        }
      },
      onStart: (item, index) => started.push([item, index]),
      onSuccess: (item, result, index) => succeeded.push([item, result, index])
    })

    expect(started).toEqual([[10, 0], [20, 1]])
    expect(active).toBe(2)

    gates.get(10)!.resolve('result-10')
    await waitFor(() => started.length === 3)
    expect(started[2]).toEqual([30, 2])
    expect(active).toBe(2)

    gates.get(20)!.resolve('result-20')
    await waitFor(() => started.length === 4)
    expect(started[3]).toEqual([40, 3])
    expect(active).toBe(2)

    gates.get(30)!.resolve('result-30')
    gates.get(40)!.resolve('result-40')
    await scanPromise

    expect(peakActive).toBe(2)
    expect(seenSignals).toEqual(items.map(() => controller.signal))
    expect(succeeded).toEqual([
      [10, 'result-10', 0],
      [20, 'result-20', 1],
      [30, 'result-30', 2],
      [40, 'result-40', 3]
    ])
  })

  it('reports success and failure callbacks and continues after an item fails', async () => {
    const controller = new AbortController()
    const failure = new Error('quota request failed')
    const events: string[] = []

    await runConcurrentScan(['first', 'second', 'third'], {
      concurrency: 1,
      signal: controller.signal,
      scan: async (item) => {
        if (item === 'second') throw failure
        return item.toUpperCase()
      },
      onStart: (item, index) => events.push(`start:${item}:${index}`),
      onSuccess: (item, result, index) => events.push(`success:${item}:${result}:${index}`),
      onError: (item, error, index) => {
        expect(error).toBe(failure)
        events.push(`error:${item}:${index}`)
      }
    })

    expect(events).toEqual([
      'start:first:0',
      'success:first:FIRST:0',
      'start:second:1',
      'error:second:1',
      'start:third:2',
      'success:third:THIRD:2'
    ])
  })

  it('does not schedule queued items after the signal is aborted', async () => {
    const controller = new AbortController()
    const firstGate = createDeferred<void>()
    const secondGate = createDeferred<void>()
    const started: number[] = []
	const succeeded: number[] = []

    const scanPromise = runConcurrentScan([1, 2, 3, 4], {
      concurrency: 2,
      signal: controller.signal,
      scan: (item) => (item === 1 ? firstGate.promise : secondGate.promise),
		onStart: (item) => started.push(item),
		onSuccess: (item) => succeeded.push(item)
    })

    expect(started).toEqual([1, 2])
    controller.abort()
    firstGate.resolve()
    secondGate.resolve()
    await scanPromise

    expect(started).toEqual([1, 2])
	expect(succeeded).toEqual([])
  })

  it('does no work when the signal is already aborted', async () => {
    const controller = new AbortController()
    controller.abort()
    const started: number[] = []

    await runConcurrentScan([1, 2], {
      concurrency: 2,
      signal: controller.signal,
      scan: async (item) => item,
      onStart: (item) => started.push(item)
    })

    expect(started).toEqual([])
  })
})
