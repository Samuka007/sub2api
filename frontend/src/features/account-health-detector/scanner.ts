export interface ConcurrentScanCallbacks<T, R> {
  onStart?: (item: T, index: number) => void
  onSuccess?: (item: T, result: R, index: number) => void
  onError?: (item: T, error: unknown, index: number) => void
}

export interface ConcurrentScanOptions<T, R> extends ConcurrentScanCallbacks<T, R> {
  concurrency: number
  signal: AbortSignal
  scan: (item: T, signal: AbortSignal) => Promise<R>
}

interface ErrorLike {
  code?: string
  name?: string
}

export function isCancelledScanError(error: unknown, signal?: AbortSignal): boolean {
  if (signal?.aborted) return true
  if (!error || typeof error !== 'object') return false
  const candidate = error as ErrorLike
  return candidate.code === 'ERR_CANCELED' || candidate.name === 'AbortError'
}

export async function runConcurrentScan<T, R>(
  items: readonly T[],
  options: ConcurrentScanOptions<T, R>
): Promise<void> {
  if (items.length === 0 || options.signal.aborted) return

  const concurrency = Math.max(1, Math.min(Math.floor(options.concurrency) || 1, items.length))
  let cursor = 0

  const nextIndex = (): number | null => {
    if (options.signal.aborted || cursor >= items.length) return null
    const index = cursor
    cursor += 1
    return index
  }

  const worker = async () => {
    while (!options.signal.aborted) {
      const index = nextIndex()
      if (index === null) return
      const item = items[index]
      options.onStart?.(item, index)

      try {
        const result = await options.scan(item, options.signal)
        if (options.signal.aborted) return
        options.onSuccess?.(item, result, index)
      } catch (error) {
        options.onError?.(item, error, index)
      }
    }
  }

  await Promise.all(Array.from({ length: concurrency }, () => worker()))
}
