const CATFK_API_BASE_URL = 'https://catfk.com'
const CATFK_SHOP_TOKEN = '3CUH5OLT'
const CATFK_VISITOR_STORAGE_KEY = 'catfk_store_visitor_id'

interface CatfkApiResponse<T> {
  code: number
  msg: string
  time: number
  data: T
}

interface CatfkCategory {
  id: number
  name: string
  image: string
  goods_count: number
}

interface CatfkGoodsListResponse {
  total: number
  list: CatfkProduct[]
}

export interface CatfkProduct {
  link: string
  goods_type: 'card' | string
  goods_key: string
  name: string
  price: number
  market_price: number
  description: string
  image: string
  category: {
    id: number
    name: string
  }
  extend: {
    stock_count: number
    show_stock_type: number
    send_order: number
    limit_count: number
    query_password_status: number
  }
}

export interface CatfkPaymentChannel {
  id: number
  name: string
  code: string
  show_name: string
  status: number
  rate: number
  paytype: {
    name: string
    icon: string
  }
}

export interface CatfkPriceQuote {
  original_amount: number
  total_amount: number
  fee: number
  fee_payer: number
  coupon_available: number
  coupon_price: number
}

export interface CatfkCreatedOrder {
  trade_no: string
  total_amount: number
  payurl: string
}

export interface CatfkOrderInfo {
  trade_no: string
  status: number
  goods_name: string
  total_amount: number
  response?: {
    cards?: string[]
    export_cards_url?: string
  }
}

const productImageByKey: Record<string, string> = {
  ve2u60: '/assets/images/product-catfk-balance-10-20260729.jpeg',
  qqtxf4: '/assets/images/product-catfk-balance-20-20260729.jpeg',
  anpm4n: '/assets/images/product-catfk-balance-50-20260729.jpeg',
  gnsrjg: '/assets/images/product-catfk-balance-100-20260729.jpeg',
  xnx3sm: '/assets/images/product-catfk-balance-200-20260729.jpeg',
  xi221g: '/assets/images/product-catfk-balance-500-20260729.jpeg',
}

export function getCatfkProductImage(goodsKey: string): string {
  return productImageByKey[goodsKey] || ''
}

function createVisitorId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID().replace(/-/g, '')
  }
  return `scitrace${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`
}

function getVisitorId(): string {
  const existing = localStorage.getItem(CATFK_VISITOR_STORAGE_KEY)
  if (existing) {
    return existing
  }

  const visitorId = createVisitorId()
  localStorage.setItem(CATFK_VISITOR_STORAGE_KEY, visitorId)
  return visitorId
}

async function catfkRequest<T>(
  path: string,
  payload: Record<string, unknown>,
  options: { allowFailureCode?: boolean; signal?: AbortSignal } = {},
): Promise<CatfkApiResponse<T>> {
  const response = await fetch(`${CATFK_API_BASE_URL}${path}`, {
    method: 'POST',
    mode: 'cors',
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      Visitorid: getVisitorId(),
    },
    body: JSON.stringify(payload),
    signal: options.signal,
  })

  if (!response.ok) {
    throw new Error(`商品服务请求失败（${response.status}）`)
  }

  const result = await response.json() as CatfkApiResponse<T>
  if (result.code !== 1 && !options.allowFailureCode) {
    throw new Error(result.msg || '商品服务暂时不可用')
  }
  return result
}

export async function getCatfkProducts(signal?: AbortSignal): Promise<CatfkProduct[]> {
  const categoryResponse = await catfkRequest<CatfkCategory[]>('/shopApi/Shop/categoryList', {
    token: CATFK_SHOP_TOKEN,
    goods_type: 'card',
    category_key: null,
  }, { signal })

  const categoryId = categoryResponse.data[0]?.id
  if (!categoryId) {
    return []
  }

  const goodsResponse = await catfkRequest<CatfkGoodsListResponse>('/shopApi/Shop/goodsList', {
    token: CATFK_SHOP_TOKEN,
    keywords: '',
    category_id: categoryId,
    goods_type: 'card',
    current: 1,
    pageSize: 20,
  }, { signal })

  return [...goodsResponse.data.list].sort((left, right) => left.price - right.price)
}

export async function getCatfkPaymentChannels(signal?: AbortSignal): Promise<CatfkPaymentChannel[]> {
  const response = await catfkRequest<CatfkPaymentChannel[]>('/shopApi/Shop/getUserChannel', {
    token: CATFK_SHOP_TOKEN,
  }, { signal })

  return response.data.filter((channel) => channel.status === 1)
}

export async function getCatfkPriceQuote(
  goodsKey: string,
  channelId: number,
  quantity: number,
  signal?: AbortSignal,
): Promise<CatfkPriceQuote> {
  const normalizedQuantity = validateQuantity(quantity)
  const response = await catfkRequest<CatfkPriceQuote>('/shopApi/Shop/getGoodsPrice', {
    goods_key: goodsKey,
    quantity: normalizedQuantity,
    coupon_code: '',
    channel_id: channelId,
  }, { signal })

  return response.data
}

export async function createCatfkOrder(input: {
  goodsKey: string
  channelId: number
  contact: string
  quantity: number
}): Promise<CatfkCreatedOrder> {
  const normalizedQuantity = validateQuantity(input.quantity)
  const response = await catfkRequest<CatfkCreatedOrder>('/shopApi/Pay/order', {
    goods_key: input.goodsKey,
    quantity: normalizedQuantity,
    coupon_code: '',
    channel_id: input.channelId,
    contact: input.contact,
    query_password: '',
    select_cards_ids: [],
    extend: {},
  })

  return response.data
}

function validateQuantity(quantity: number): number {
  if (!Number.isInteger(quantity) || quantity < 1) {
    throw new Error('商品数量必须是大于 0 的整数')
  }
  return quantity
}

export async function isCatfkOrderPaid(tradeNo: string): Promise<boolean> {
  const response = await catfkRequest<unknown>('/shopApi/Pay/query', {
    trade_no: tradeNo,
  }, { allowFailureCode: true })

  return response.code === 1
}

export async function getCatfkOrderInfo(tradeNo: string): Promise<CatfkOrderInfo> {
  const response = await catfkRequest<CatfkOrderInfo>('/shopApi/Order/info', {
    trade_no: tradeNo,
    query_password: '',
    dump: 1,
  })

  return response.data
}

export const catfkStoreAPI = {
  getProducts: getCatfkProducts,
  getPaymentChannels: getCatfkPaymentChannels,
  getPriceQuote: getCatfkPriceQuote,
  createOrder: createCatfkOrder,
  isOrderPaid: isCatfkOrderPaid,
  getOrderInfo: getCatfkOrderInfo,
}

export default catfkStoreAPI
