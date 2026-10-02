export function applyRateAdjustment(
  rate: number,
  multiplier: number | null | undefined,
  addition: number | null | undefined
): number {
  return Number((rate * (multiplier ?? 1) + (addition ?? 0)).toFixed(6))
}
