<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-5">
      <header class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 class="mb-2 text-2xl font-bold text-gray-950 dark:text-white">兑换码直购</h1>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            当前账户：{{ authStore.user?.username || '-' }}
          </p>
        </div>
        <div class="border-l-2 border-[#238b76] pl-4 sm:text-right">
          <p class="text-xs font-medium text-gray-400 dark:text-gray-500">当前余额</p>
          <p class="mt-1 text-xl font-bold text-gray-950 dark:text-white">
            ${{ (authStore.user?.balance || 0).toFixed(2) }}
          </p>
        </div>
      </header>

      <div
        v-if="activeOrder"
        class="flex flex-col gap-4 border-y border-[#b8ddd4] bg-[#edf8f5] px-4 py-4 sm:flex-row sm:items-center sm:justify-between dark:border-[#28564d] dark:bg-[#102822]"
        aria-live="polite"
      >
        <div class="flex min-w-0 items-center gap-3">
          <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-white text-[#238b76] dark:bg-[#183a32] dark:text-[#78d8c2]">
            <Icon :name="activeOrder.status === 'completed' ? 'checkCircle' : 'clock'" size="md" />
          </span>
          <div class="min-w-0">
            <p class="font-semibold text-gray-950 dark:text-white">
              {{ activeOrder.status === 'completed' ? '订单已完成' : '等待支付确认' }}
            </p>
            <p class="truncate text-sm text-gray-500 dark:text-gray-400">
              {{ activeOrder.product.name }} × {{ activeOrder.quantity }} · {{ activeOrder.tradeNo }}
            </p>
          </div>
        </div>
        <button
          type="button"
          class="inline-flex min-h-10 items-center justify-center gap-2 rounded-md border border-[#75b7a7] bg-white px-4 text-sm font-semibold text-[#176b59] transition-colors hover:bg-[#f7fcfa] active:scale-[0.98] dark:border-[#3d7d6e] dark:bg-[#17342e] dark:text-[#8de0cb] dark:hover:bg-[#1c4038]"
          @click="showCheckout = true"
        >
          查看订单
          <Icon name="chevronRight" size="sm" />
        </button>
      </div>

      <section aria-labelledby="catfk-products-title">
        <div class="mb-4 flex items-center justify-between gap-4 border-b border-gray-200 pb-4 dark:border-dark-700">
          <div>
            <h2 id="catfk-products-title" class="text-lg font-bold text-gray-950 dark:text-white">API 额度兑换码</h2>
            <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">
              数据来源：SciTrace 云猫寄售店铺
              <span v-if="lastUpdated"> · {{ lastUpdated }}</span>
            </p>
          </div>
          <button
            type="button"
            class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-md border border-gray-200 bg-white text-gray-500 transition-colors hover:border-[#8fcabc] hover:text-[#238b76] active:scale-[0.96] disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400 dark:hover:border-[#35695e] dark:hover:text-[#78d8c2]"
            :disabled="loadingProducts"
            title="刷新商品"
            aria-label="刷新商品"
            @click="loadProducts"
          >
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': loadingProducts }" />
          </button>
        </div>

        <div v-if="loadingProducts" class="grid gap-4 md:grid-cols-2" aria-label="正在加载商品">
          <div v-for="index in 4" :key="index" class="grid min-h-52 grid-cols-[104px_minmax(0,1fr)] gap-4 rounded-lg border border-gray-200 bg-white p-4 sm:grid-cols-[148px_minmax(0,1fr)] dark:border-dark-700 dark:bg-dark-900">
            <div class="aspect-[3/4] animate-pulse rounded-md bg-gray-100 dark:bg-dark-800"></div>
            <div class="flex flex-col py-1">
              <div class="h-5 w-2/3 animate-pulse rounded bg-gray-100 dark:bg-dark-800"></div>
              <div class="mt-3 h-4 w-1/2 animate-pulse rounded bg-gray-100 dark:bg-dark-800"></div>
              <div class="mt-auto h-10 w-full animate-pulse rounded bg-gray-100 dark:bg-dark-800"></div>
            </div>
          </div>
        </div>

        <div
          v-else-if="productError"
          class="flex min-h-56 flex-col items-center justify-center border-y border-red-200 bg-red-50/70 px-6 text-center dark:border-red-900/60 dark:bg-red-950/20"
          role="alert"
        >
          <Icon name="exclamationCircle" size="lg" class="text-red-500" />
          <p class="mt-3 font-semibold text-gray-950 dark:text-white">商品加载失败</p>
          <p class="mt-1 max-w-xl text-sm text-gray-500 dark:text-gray-400">{{ productError }}</p>
          <button type="button" class="btn btn-secondary mt-4" @click="loadProducts">重新加载</button>
        </div>

        <div
          v-else-if="products.length === 0"
          class="flex min-h-56 flex-col items-center justify-center border-y border-gray-200 px-6 text-center dark:border-dark-700"
        >
          <Icon name="inbox" size="lg" class="text-gray-300 dark:text-dark-600" />
          <p class="mt-3 font-semibold text-gray-700 dark:text-gray-300">暂无可购买商品</p>
        </div>

        <div v-else class="grid gap-4 md:grid-cols-2">
          <article
            v-for="product in products"
            :key="product.goods_key"
            :data-testid="`catfk-product-${product.goods_key}`"
            class="grid min-h-52 grid-cols-[104px_minmax(0,1fr)] gap-4 overflow-hidden rounded-lg border border-gray-200 bg-white p-4 shadow-sm transition-[border-color,box-shadow,transform] duration-200 hover:border-[#9bcfc3] hover:shadow-md sm:grid-cols-[148px_minmax(0,1fr)] dark:border-dark-700 dark:bg-dark-900 dark:hover:border-[#35695e]"
          >
            <div class="aspect-[3/4] overflow-hidden rounded-md bg-[#f3efe7] dark:bg-dark-800">
              <img
                v-if="getCatfkProductImage(product.goods_key)"
                :src="getCatfkProductImage(product.goods_key)"
                :alt="product.name"
                class="h-full w-full object-cover"
              />
              <div v-else class="flex h-full items-center justify-center text-gray-300 dark:text-dark-600">
                <Icon name="gift" size="xl" />
              </div>
            </div>

            <div class="flex min-w-0 flex-col py-1">
              <div>
                <div class="flex flex-wrap items-start justify-between gap-2">
                  <span class="rounded bg-[#e9f5f2] px-2 py-1 text-xs font-semibold text-[#227763] dark:bg-[#17342e] dark:text-[#83d9c4]">
                    {{ product.category.name }}
                  </span>
                  <span
                    class="text-xs font-medium"
                    :class="product.extend.stock_count > 0 ? 'text-gray-500 dark:text-gray-400' : 'text-red-500'"
                  >
                    {{ product.extend.stock_count > 0 ? `库存 ${product.extend.stock_count}` : '暂时售罄' }}
                  </span>
                </div>
                <h3 class="mt-3 break-words text-base font-bold leading-snug text-gray-950 sm:text-lg dark:text-white">
                  {{ product.name }}
                </h3>
                <p class="mt-2 text-2xl font-bold text-[#167563] dark:text-[#76d7c0]">
                  <span class="text-sm">¥</span>{{ product.price.toFixed(2) }}
                </p>
              </div>

              <button
                type="button"
                class="mt-auto inline-flex min-h-10 w-full items-center justify-center gap-2 rounded-md bg-[#167563] px-4 py-2 text-sm font-semibold text-white transition-[background-color,transform] hover:bg-[#0f6555] active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-gray-300 dark:bg-[#58bea7] dark:text-[#09271f] dark:hover:bg-[#72ceb8] dark:disabled:bg-dark-700 dark:disabled:text-dark-500"
                :disabled="product.extend.stock_count <= 0"
                :data-testid="`buy-${product.goods_key}`"
                @click="openCheckout(product)"
              >
                购买兑换码
                <Icon name="arrowRight" size="sm" />
              </button>
            </div>
          </article>
        </div>
      </section>

      <section
        class="flex flex-col gap-6 rounded-lg border border-[#b9ddd4] bg-[#fbfefd] p-6 sm:flex-row sm:items-center sm:justify-between sm:p-7 dark:border-[#36554f] dark:bg-[#121f1c]"
        aria-labelledby="redeem-entry-title"
      >
        <div class="flex min-w-0 items-start gap-4 sm:items-center">
          <span class="flex h-14 w-14 shrink-0 items-center justify-center rounded-lg bg-[#e6f5f1] text-[#167563] dark:bg-[#1b3b34] dark:text-[#76ddc5]">
            <Icon name="gift" size="md" :stroke-width="1.9" />
          </span>
          <div class="min-w-0">
            <h2 id="redeem-entry-title" class="text-lg font-bold leading-snug text-[#1d2926] sm:text-xl dark:text-[#f1f7f5]">
              兑换入口
            </h2>
            <p class="mt-2 text-sm leading-relaxed text-[#53645f] sm:text-base dark:text-[#acc0bb]">
              已经购买兑换码？前往兑换页面，将兑换码兑换为 API 额度。
            </p>
          </div>
        </div>
        <button
          data-testid="redeem-entry-button"
          type="button"
          class="inline-flex min-h-12 w-full shrink-0 items-center justify-center gap-2 rounded-lg bg-[#167563] px-6 py-3 text-base font-semibold text-white shadow-[0_7px_18px_rgba(22,117,99,0.24)] outline-none transition-[background-color,box-shadow,transform] duration-200 hover:bg-[#0f6555] hover:shadow-[0_9px_22px_rgba(22,117,99,0.3)] active:scale-[0.98] sm:w-auto focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#167563] dark:bg-[#53bda5] dark:text-[#0c2923] dark:hover:bg-[#6bcbb5] dark:focus-visible:outline-[#76ddc5]"
          @click="goToRedeem"
        >
          兑换入口
          <Icon name="arrowRight" size="sm" :stroke-width="1.9" />
        </button>
      </section>
    </div>

    <BaseDialog
      :show="showCheckout"
      :title="checkoutTitle"
      width="normal"
      :close-on-escape="!checkoutBusy"
      :show-close-button="!checkoutBusy"
      @close="closeCheckout"
    >
      <div v-if="selectedProduct" class="space-y-5" data-testid="catfk-checkout-dialog">
        <div class="grid grid-cols-[72px_minmax(0,1fr)] gap-4 border-b border-gray-200 pb-5 dark:border-dark-700">
          <div class="aspect-[3/4] overflow-hidden rounded-md bg-[#f3efe7] dark:bg-dark-800">
            <img
              v-if="getCatfkProductImage(selectedProduct.goods_key)"
              :src="getCatfkProductImage(selectedProduct.goods_key)"
              :alt="selectedProduct.name"
              class="h-full w-full object-cover"
            />
          </div>
          <div class="min-w-0 self-center">
            <p class="break-words font-semibold text-gray-950 dark:text-white">{{ selectedProduct.name }}</p>
            <p class="mt-1 text-xl font-bold text-[#167563] dark:text-[#76d7c0]">
              ¥{{ selectedProduct.price.toFixed(2) }}
              <span class="text-xs font-medium text-gray-400 dark:text-gray-500">/ 件</span>
            </p>
          </div>
        </div>

        <template v-if="checkoutStep === 'confirm'">
          <div v-if="checkoutLoading" class="flex min-h-40 items-center justify-center">
            <span class="h-7 w-7 animate-spin rounded-full border-2 border-[#238b76] border-t-transparent"></span>
          </div>

          <template v-else>
            <div>
              <label for="catfk-contact" class="mb-2 block text-sm font-semibold text-gray-700 dark:text-gray-300">订单联系方式</label>
              <input
                id="catfk-contact"
                v-model.trim="contact"
                type="text"
                autocomplete="email"
                class="input w-full"
                :class="{ 'border-red-400 focus:border-red-500 focus:ring-red-500/20': contactError }"
                @input="contactError = ''"
              />
              <p v-if="contactError" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ contactError }}</p>
            </div>

            <div class="flex items-end justify-between gap-4">
              <div>
                <label id="catfk-quantity-label" for="catfk-quantity" class="block text-sm font-semibold text-gray-700 dark:text-gray-300">购买数量</label>
                <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">
                  {{ minimumQuantity }} 件起购，当前库存 {{ maximumQuantity }} 件
                </p>
              </div>
              <div class="grid h-11 shrink-0 grid-cols-[44px_56px_44px] overflow-hidden rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800" role="group" aria-labelledby="catfk-quantity-label">
                <button
                  type="button"
                  class="inline-flex h-full items-center justify-center border-r border-gray-200 text-gray-500 outline-none transition-[background-color,color,transform] hover:bg-gray-50 hover:text-[#167563] active:scale-[0.96] disabled:cursor-not-allowed disabled:text-gray-300 disabled:hover:bg-transparent focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-[#167563] dark:border-dark-700 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-[#76d7c0] dark:disabled:text-dark-600"
                  :disabled="quantity <= minimumQuantity"
                  aria-label="减少购买数量"
                  data-testid="decrease-quantity"
                  @click="changeQuantity(quantity - 1)"
                >
                  <Icon name="minus" size="sm" :stroke-width="2" />
                </button>
                <input
                  id="catfk-quantity"
                  :value="quantity"
                  type="number"
                  inputmode="numeric"
                  :min="minimumQuantity"
                  :max="maximumQuantity"
                  class="h-full w-full appearance-none border-0 bg-transparent p-0 text-center text-sm font-semibold text-gray-900 outline-none focus:ring-0 dark:text-white"
                  aria-labelledby="catfk-quantity-label"
                  data-testid="quantity-input"
                  @change="handleQuantityInput"
                />
                <button
                  type="button"
                  class="inline-flex h-full items-center justify-center border-l border-gray-200 text-gray-500 outline-none transition-[background-color,color,transform] hover:bg-gray-50 hover:text-[#167563] active:scale-[0.96] disabled:cursor-not-allowed disabled:text-gray-300 disabled:hover:bg-transparent focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-[#167563] dark:border-dark-700 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-[#76d7c0] dark:disabled:text-dark-600"
                  :disabled="quantity >= maximumQuantity"
                  aria-label="增加购买数量"
                  data-testid="increase-quantity"
                  @click="changeQuantity(quantity + 1)"
                >
                  <Icon name="plus" size="sm" :stroke-width="2" />
                </button>
              </div>
            </div>

            <fieldset>
              <legend class="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-300">支付方式</legend>
              <div class="grid grid-cols-2 gap-3">
                <button
                  v-for="channel in channels"
                  :key="channel.id"
                  type="button"
                  class="flex min-h-16 items-center gap-3 rounded-md border px-3 py-3 text-left transition-[border-color,background-color,transform] active:scale-[0.98]"
                  :class="selectedChannelId === channel.id
                    ? 'border-[#238b76] bg-[#edf8f5] text-[#165f50] dark:border-[#63c6ae] dark:bg-[#15352e] dark:text-[#91e2ce]'
                    : 'border-gray-200 bg-white text-gray-600 hover:border-gray-300 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-dark-600'"
                  :aria-pressed="selectedChannelId === channel.id"
                  :data-testid="`channel-${channel.id}`"
                  @click="selectChannel(channel.id)"
                >
                  <Icon name="creditCard" size="md" class="shrink-0" />
                  <span class="min-w-0">
                    <span class="block truncate text-sm font-semibold">{{ channel.show_name }}</span>
                    <span class="mt-0.5 block text-xs opacity-70">费率 {{ channel.rate }}%</span>
                  </span>
                </button>
              </div>
            </fieldset>

            <div class="divide-y divide-gray-100 rounded-md border border-gray-200 bg-gray-50 px-4 text-sm dark:divide-dark-700 dark:border-dark-700 dark:bg-dark-800/70">
              <div class="flex items-center justify-between py-3">
                <span class="text-gray-500 dark:text-gray-400">商品金额</span>
                <span class="font-medium text-gray-900 dark:text-white">¥{{ productSubtotal.toFixed(2) }}</span>
              </div>
              <div class="flex items-center justify-between py-3">
                <span class="text-gray-500 dark:text-gray-400">应付金额</span>
                <span class="text-lg font-bold text-[#167563] dark:text-[#76d7c0]">
                  {{ quoteLoading ? '计算中' : `¥${(quote?.total_amount ?? productSubtotal).toFixed(2)}` }}
                </span>
              </div>
            </div>

            <div v-if="checkoutError" class="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300" role="alert">
              {{ checkoutError }}
            </div>

            <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
              <button type="button" class="btn btn-secondary min-h-11 sm:min-w-24" @click="closeCheckout">取消</button>
              <button
                type="button"
                class="inline-flex min-h-11 items-center justify-center gap-2 rounded-md bg-[#167563] px-5 py-2.5 text-sm font-semibold text-white transition-[background-color,transform] hover:bg-[#0f6555] active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-50 dark:bg-[#58bea7] dark:text-[#09271f] dark:hover:bg-[#72ceb8]"
                :disabled="!selectedChannelId || quoteLoading"
                data-testid="confirm-catfk-order"
                @click="confirmOrder"
              >
                确认并打开支付
                <Icon name="externalLink" size="sm" />
              </button>
            </div>
          </template>
        </template>

        <div v-else-if="checkoutStep === 'creating'" class="flex min-h-52 flex-col items-center justify-center text-center" aria-live="polite">
          <span class="h-8 w-8 animate-spin rounded-full border-2 border-[#238b76] border-t-transparent"></span>
          <p class="mt-4 font-semibold text-gray-950 dark:text-white">正在创建订单</p>
        </div>

        <div v-else-if="checkoutStep === 'paying'" class="flex min-h-52 flex-col items-center justify-center text-center" aria-live="polite">
          <span class="flex h-14 w-14 items-center justify-center rounded-lg bg-[#edf8f5] text-[#238b76] dark:bg-[#15352e] dark:text-[#82dcc7]">
            <Icon name="clock" size="lg" />
          </span>
          <p class="mt-4 text-lg font-bold text-gray-950 dark:text-white">等待付款</p>
          <p class="mt-2 break-all text-xs text-gray-400 dark:text-gray-500">订单号：{{ activeOrder?.tradeNo }}</p>
          <div class="mt-5 flex w-full flex-col gap-3 sm:flex-row sm:justify-center">
            <button type="button" class="btn btn-secondary min-h-11" :disabled="checkingOrder" @click="checkOrderNow(true)">
              {{ checkingOrder ? '查询中' : '我已完成付款' }}
            </button>
            <button type="button" class="btn btn-primary min-h-11" @click="openPaymentPage">
              重新打开支付页
            </button>
          </div>
          <p v-if="checkoutError" class="mt-4 text-sm text-red-600 dark:text-red-400">{{ checkoutError }}</p>
        </div>

        <div v-else-if="checkoutStep === 'redeeming'" class="flex min-h-52 flex-col items-center justify-center text-center" aria-live="polite">
          <span class="h-8 w-8 animate-spin rounded-full border-2 border-[#238b76] border-t-transparent"></span>
          <p class="mt-4 font-semibold text-gray-950 dark:text-white">付款成功，正在充入账户</p>
        </div>

        <div v-else-if="checkoutStep === 'success'" class="flex min-h-52 flex-col items-center justify-center text-center" aria-live="polite">
          <span class="flex h-14 w-14 items-center justify-center rounded-lg bg-emerald-50 text-emerald-600 dark:bg-emerald-950/40 dark:text-emerald-400">
            <Icon name="checkCircle" size="lg" />
          </span>
          <p class="mt-4 text-lg font-bold text-gray-950 dark:text-white">充值完成</p>
          <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">兑换码已自动充入当前账户</p>
          <button type="button" class="btn btn-primary mt-5 min-h-11 min-w-28" @click="closeCompletedOrder">完成</button>
        </div>

        <div v-else class="min-h-52" aria-live="polite">
          <div class="flex flex-col items-center text-center">
            <Icon name="exclamationTriangle" size="lg" class="text-amber-500" />
            <p class="mt-3 text-lg font-bold text-gray-950 dark:text-white">自动兑换未完成</p>
            <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ checkoutError }}</p>
          </div>
          <div v-if="retrievedCodes.length" class="mt-5 space-y-2">
            <div v-for="code in retrievedCodes" :key="code" class="flex items-center gap-2 rounded-md border border-gray-200 bg-gray-50 p-3 dark:border-dark-700 dark:bg-dark-800">
              <code class="min-w-0 flex-1 break-all text-xs text-gray-700 dark:text-gray-300">{{ code }}</code>
              <button type="button" class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-gray-500 hover:bg-white hover:text-[#238b76] dark:hover:bg-dark-700" title="复制兑换码" @click="copyCode(code)">
                <Icon name="copy" size="sm" />
              </button>
            </div>
          </div>
          <div class="mt-5 flex flex-col gap-3 sm:flex-row sm:justify-end">
            <button type="button" class="btn btn-secondary min-h-11" @click="goToRedeem">前往兑换入口</button>
            <button v-if="retrievedCodes.length" type="button" class="btn btn-primary min-h-11" @click="retryRedeem">重新自动兑换</button>
          </div>
        </div>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import redeemAPI from '@/api/redeem'
import catfkStoreAPI, {
  getCatfkProductImage,
  type CatfkCreatedOrder,
  type CatfkPaymentChannel,
  type CatfkPriceQuote,
  type CatfkProduct,
} from '@/api/catfkStore'

type CheckoutStep = 'confirm' | 'creating' | 'paying' | 'redeeming' | 'success' | 'failed'

interface ActiveStoreOrder {
  tradeNo: string
  payUrl: string
  product: CatfkProduct
  quantity: number
  status: 'pending' | 'completed'
}

const router = useRouter()
const authStore = useAuthStore()
const appStore = useAppStore()

const products = ref<CatfkProduct[]>([])
const loadingProducts = ref(true)
const productError = ref('')
const lastUpdated = ref('')
const showCheckout = ref(false)
const selectedProduct = ref<CatfkProduct | null>(null)
const channels = ref<CatfkPaymentChannel[]>([])
const selectedChannelId = ref<number | null>(null)
const quantity = ref(1)
const quote = ref<CatfkPriceQuote | null>(null)
const contact = ref('')
const contactError = ref('')
const checkoutError = ref('')
const checkoutLoading = ref(false)
const quoteLoading = ref(false)
const checkoutStep = ref<CheckoutStep>('confirm')
const activeOrder = ref<ActiveStoreOrder | null>(null)
const checkingOrder = ref(false)
const fulfillmentStarted = ref(false)
const retrievedCodes = ref<string[]>([])
const redeemedCodes = ref<string[]>([])

let productsController: AbortController | null = null
let checkoutController: AbortController | null = null
let pollTimer: ReturnType<typeof setInterval> | null = null
let quoteRequestId = 0

const checkoutBusy = computed(() => ['creating', 'redeeming'].includes(checkoutStep.value))
const minimumQuantity = computed(() => Math.max(1, Math.trunc(selectedProduct.value?.extend.limit_count || 1)))
const maximumQuantity = computed(() => {
  const stock = Math.max(0, Math.trunc(selectedProduct.value?.extend.stock_count || 0))
  return Math.max(minimumQuantity.value, stock)
})
const productSubtotal = computed(() => (selectedProduct.value?.price || 0) * quantity.value)
const checkoutTitle = computed(() => {
  if (checkoutStep.value === 'success') return '充值完成'
  if (checkoutStep.value === 'failed') return '订单处理'
  if (checkoutStep.value === 'paying') return '等待付款'
  return '确认购买'
})

function errorMessage(error: unknown): string {
  if (error instanceof DOMException && error.name === 'AbortError') {
    return ''
  }
  return error instanceof Error ? error.message : '请求失败，请稍后重试'
}

async function loadProducts(): Promise<void> {
  productsController?.abort()
  productsController = new AbortController()
  loadingProducts.value = true
  productError.value = ''

  try {
    products.value = await catfkStoreAPI.getProducts(productsController.signal)
    lastUpdated.value = new Intl.DateTimeFormat('zh-CN', {
      hour: '2-digit',
      minute: '2-digit',
    }).format(new Date())
  } catch (error) {
    const message = errorMessage(error)
    if (message) productError.value = message
  } finally {
    loadingProducts.value = false
  }
}

async function openCheckout(product: CatfkProduct): Promise<void> {
  selectedProduct.value = product
  quantity.value = Math.max(1, Math.trunc(product.extend.limit_count || 1))
  showCheckout.value = true
  checkoutStep.value = 'confirm'
  checkoutLoading.value = true
  checkoutError.value = ''
  contactError.value = ''
  contact.value = authStore.user?.email || authStore.user?.username || ''
  channels.value = []
  selectedChannelId.value = null
  quote.value = null
  retrievedCodes.value = []
  redeemedCodes.value = []

  checkoutController?.abort()
  checkoutController = new AbortController()

  try {
    channels.value = await catfkStoreAPI.getPaymentChannels(checkoutController.signal)
    selectedChannelId.value = channels.value[0]?.id ?? null
    if (selectedChannelId.value) {
      await refreshQuote(selectedChannelId.value, checkoutController.signal)
    }
  } catch (error) {
    const message = errorMessage(error)
    if (message) checkoutError.value = message
  } finally {
    checkoutLoading.value = false
  }
}

async function refreshQuote(channelId: number, signal?: AbortSignal): Promise<void> {
  const product = selectedProduct.value
  if (!product) return

  const requestId = ++quoteRequestId
  quoteLoading.value = true
  try {
    const nextQuote = await catfkStoreAPI.getPriceQuote(product.goods_key, channelId, quantity.value, signal)
    if (requestId === quoteRequestId) quote.value = nextQuote
  } catch (error) {
    if (requestId === quoteRequestId) checkoutError.value = errorMessage(error)
  } finally {
    if (requestId === quoteRequestId) quoteLoading.value = false
  }
}

async function selectChannel(channelId: number): Promise<void> {
  selectedChannelId.value = channelId
  checkoutError.value = ''
  await refreshQuote(channelId)
}

function changeQuantity(nextQuantity: number): void {
  const integerQuantity = Number.isFinite(nextQuantity) ? Math.trunc(nextQuantity) : minimumQuantity.value
  const nextValue = Math.min(maximumQuantity.value, Math.max(minimumQuantity.value, integerQuantity))
  if (nextValue === quantity.value) return

  quantity.value = nextValue
  quote.value = null
  checkoutError.value = ''
  if (selectedChannelId.value) {
    void refreshQuote(selectedChannelId.value)
  }
}

function handleQuantityInput(event: Event): void {
  const input = event.currentTarget as HTMLInputElement
  changeQuantity(Number(input.value))
  input.value = String(quantity.value)
}

async function confirmOrder(): Promise<void> {
  const product = selectedProduct.value
  const channelId = selectedChannelId.value
  const normalizedContact = contact.value.trim()

  if (!normalizedContact) {
    contactError.value = '请输入订单联系方式'
    return
  }
  if (!product || !channelId) {
    checkoutError.value = '请选择支付方式'
    return
  }
  if (quantity.value < minimumQuantity.value || quantity.value > maximumQuantity.value) {
    checkoutError.value = `购买数量必须在 ${minimumQuantity.value} 到 ${maximumQuantity.value} 之间`
    return
  }

  checkoutStep.value = 'creating'
  checkoutError.value = ''
  const paymentWindow = window.open('about:blank', '_blank')
  if (paymentWindow) paymentWindow.opener = null

  try {
    const order: CatfkCreatedOrder = await catfkStoreAPI.createOrder({
      goodsKey: product.goods_key,
      channelId,
      contact: normalizedContact,
      quantity: quantity.value,
    })

    activeOrder.value = {
      tradeNo: order.trade_no,
      payUrl: order.payurl,
      product,
      quantity: quantity.value,
      status: 'pending',
    }
    checkoutStep.value = 'paying'

    if (paymentWindow && !paymentWindow.closed) {
      paymentWindow.location.href = order.payurl
    } else {
      appStore.showInfo('支付窗口被浏览器拦截，请点击“重新打开支付页”')
    }
    startPolling()
  } catch (error) {
    paymentWindow?.close()
    checkoutStep.value = 'confirm'
    checkoutError.value = errorMessage(error)
  }
}

function startPolling(): void {
  stopPolling()
  void checkOrderNow(false)
  pollTimer = setInterval(() => {
    void checkOrderNow(false)
  }, 3000)
}

function stopPolling(): void {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

async function checkOrderNow(manual: boolean): Promise<void> {
  if (!activeOrder.value || checkingOrder.value || fulfillmentStarted.value) return

  checkingOrder.value = true
  if (manual) checkoutError.value = ''

  try {
    const paid = await catfkStoreAPI.isOrderPaid(activeOrder.value.tradeNo)
    if (!paid) {
      if (manual) checkoutError.value = '暂未查询到付款结果，请稍后再试'
      return
    }
    await fulfillPaidOrder()
  } catch (error) {
    if (manual) checkoutError.value = errorMessage(error)
  } finally {
    checkingOrder.value = false
  }
}

async function fulfillPaidOrder(): Promise<void> {
  const order = activeOrder.value
  if (!order || fulfillmentStarted.value) return

  fulfillmentStarted.value = true
  stopPolling()
  showCheckout.value = true
  checkoutStep.value = 'redeeming'
  checkoutError.value = ''

  try {
    const orderInfo = await catfkStoreAPI.getOrderInfo(order.tradeNo)
    const codes = [...new Set((orderInfo.response?.cards || []).map((code) => code.trim()).filter(Boolean))]
    if (codes.length === 0) {
      throw new Error('付款已确认，但暂未取得兑换码，请稍后重试')
    }

    retrievedCodes.value = codes
    await redeemCodes(codes)
  } catch (error) {
    checkoutStep.value = 'failed'
    checkoutError.value = errorMessage(error)
  } finally {
    fulfillmentStarted.value = false
  }
}

async function redeemCodes(codes: string[]): Promise<void> {
  checkoutStep.value = 'redeeming'
  checkoutError.value = ''
  for (const code of codes) {
    if (redeemedCodes.value.includes(code)) continue
    await redeemAPI.redeem(code)
    redeemedCodes.value.push(code)
  }

  if (activeOrder.value) activeOrder.value.status = 'completed'
  checkoutStep.value = 'success'
  await authStore.refreshUser().catch(() => undefined)
  appStore.showSuccess('兑换码已充入当前账户')
}

async function retryRedeem(): Promise<void> {
  try {
    await redeemCodes(retrievedCodes.value)
  } catch (error) {
    checkoutStep.value = 'failed'
    checkoutError.value = errorMessage(error)
  }
}

function openPaymentPage(): void {
  if (!activeOrder.value?.payUrl) return
  window.open(activeOrder.value.payUrl, '_blank', 'noopener,noreferrer')
}

async function copyCode(code: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(code)
    appStore.showSuccess('兑换码已复制')
  } catch {
    appStore.showError('复制失败，请手动复制')
  }
}

function goToRedeem(): void {
  showCheckout.value = false
  void router.push('/redeem')
}

function closeCheckout(): void {
  if (checkoutBusy.value) return
  showCheckout.value = false
}

function closeCompletedOrder(): void {
  showCheckout.value = false
  activeOrder.value = null
  selectedProduct.value = null
  retrievedCodes.value = []
  redeemedCodes.value = []
}

onMounted(() => {
  void loadProducts()
})

onBeforeUnmount(() => {
  productsController?.abort()
  checkoutController?.abort()
  stopPolling()
})
</script>
