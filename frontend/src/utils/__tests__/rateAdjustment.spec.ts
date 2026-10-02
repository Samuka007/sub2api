import { describe, expect, it } from 'vitest'
import { applyRateAdjustment } from '../rateAdjustment'

describe('applyRateAdjustment', () => {
  it('supports an addition without a multiplier', () => {
    expect(applyRateAdjustment(0.8, null, 0.1)).toBe(0.9)
  })

  it('supports multiplication without an addition', () => {
    expect(applyRateAdjustment(0.8, 1.5, null)).toBe(1.2)
  })

  it('applies multiplication before addition', () => {
    expect(applyRateAdjustment(0.8, 1.5, 0.1)).toBe(1.3)
  })

  it('uses identity values when both adjustments are omitted', () => {
    expect(applyRateAdjustment(0.8, null, null)).toBe(0.8)
  })
})
