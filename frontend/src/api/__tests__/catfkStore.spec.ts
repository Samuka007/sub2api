import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createCatfkOrder,
  getCatfkPriceQuote,
  getCatfkProductImage,
  getCatfkProducts,
  isCatfkOrderPaid,
} from '@/api/catfkStore'

const fetchMock = vi.fn()

function jsonResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: vi.fn().mockResolvedValue(body),
  } as unknown as Response
}

describe('catfk store api', () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    localStorage.clear()
  })

  it('loads the card category and returns products sorted by price', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        code: 1,
        msg: 'success',
        time: 1,
        data: [{ id: 6266, name: 'stem充值兑换码', image: '', goods_count: 2 }],
      }))
      .mockResolvedValueOnce(jsonResponse({
        code: 1,
        msg: 'success',
        time: 1,
        data: {
          total: 2,
          list: [
            { goods_key: 'xi221g', price: 500 },
            { goods_key: 've2u60', price: 10 },
          ],
        },
      }))

    const products = await getCatfkProducts()

    expect(products.map((product) => product.goods_key)).toEqual(['ve2u60', 'xi221g'])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(fetchMock.mock.calls[0][0]).toBe('https://catfk.com/shopApi/Shop/categoryList')
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toMatchObject({
      token: '3CUH5OLT',
      category_id: 6266,
      goods_type: 'card',
    })
    expect(fetchMock.mock.calls[1][1].headers.Visitorid).toBeTruthy()
  })

  it('requests a provider quote for the selected quantity', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      code: 1,
      msg: 'success',
      time: 1,
      data: {
        original_amount: 40,
        total_amount: 40.72,
        fee: 0.72,
        fee_payer: 1,
        coupon_available: 0,
        coupon_price: 0,
      },
    }))

    await expect(getCatfkPriceQuote('ve2u60', 1, 4)).resolves.toMatchObject({
      original_amount: 40,
      total_amount: 40.72,
    })
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toMatchObject({
      goods_key: 've2u60',
      channel_id: 1,
      quantity: 4,
    })
  })

  it('creates a multi-card order without trusting client price data', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      code: 1,
      msg: 'success',
      time: 1,
      data: {
        trade_no: 'T20260729001',
        total_amount: 10,
        payurl: 'https://catfk.com/pay/test',
      },
    }))

    const order = await createCatfkOrder({
      goodsKey: 've2u60',
      channelId: 1,
      contact: 'buyer@example.com',
      quantity: 3,
    })

    expect(order.trade_no).toBe('T20260729001')
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      goods_key: 've2u60',
      quantity: 3,
      coupon_code: '',
      channel_id: 1,
      contact: 'buyer@example.com',
      query_password: '',
      select_cards_ids: [],
      extend: {},
    })
  })

  it('rejects invalid quantities before calling the provider', async () => {
    await expect(createCatfkOrder({
      goodsKey: 've2u60',
      channelId: 1,
      contact: 'buyer@example.com',
      quantity: 0,
    })).rejects.toThrow('商品数量必须是大于 0 的整数')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('treats the provider failure code as an unpaid polling result', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      code: 0,
      msg: 'pending',
      time: 1,
      data: null,
    }))

    await expect(isCatfkOrderPaid('T20260729002')).resolves.toBe(false)
  })

  it('uses local product images and does not hotlink catfk uploads', () => {
    expect(getCatfkProductImage('ve2u60')).toBe('/assets/images/product-catfk-balance-10-20260729.jpeg')
    expect(getCatfkProductImage('unknown-product')).toBe('')
  })
})
