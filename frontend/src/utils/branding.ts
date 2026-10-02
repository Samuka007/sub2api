import { sanitizeUrl } from '@/utils/url'

export const DEFAULT_SITE_NAME = 'SCIbuddy'

const LEGACY_DEFAULT_SITE_NAME = 'Sub2API'

export function resolveSiteName(value: unknown): string {
  const siteName = typeof value === 'string' ? value.trim() : ''
  if (!siteName || siteName === LEGACY_DEFAULT_SITE_NAME) {
    return DEFAULT_SITE_NAME
  }
  return siteName
}

export function updateFavicon(logoUrl: string): void {
  const sanitizedLogoUrl = sanitizeUrl(logoUrl, {
    allowRelative: true,
    allowDataUrl: true,
  })
  if (!sanitizedLogoUrl) {
    return
  }

  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }

  link.type = sanitizedLogoUrl.endsWith('.svg') ? 'image/svg+xml' : 'image/x-icon'
  link.href = sanitizedLogoUrl
}
