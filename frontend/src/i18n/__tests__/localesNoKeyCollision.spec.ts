import { describe, expect, it } from 'vitest'

import enAdminAccounts from '../locales/en/admin/accounts'
import enAdminChannels from '../locales/en/admin/channels'
import enAdminOps from '../locales/en/admin/ops'
import enAdminOverview from '../locales/en/admin/overview'
import enAdminPlusQuotaAutomation from '../locales/en/admin/plusQuotaAutomation'
import enAdminOneClickAccountNotes from '../locales/en/admin/oneClickAccountNotes'
import enAdminResources from '../locales/en/admin/resources'
import enAdminSettings from '../locales/en/admin/settings'
import enCommon from '../locales/en/common'
import enDashboard from '../locales/en/dashboard'
import enLanding from '../locales/en/landing'
import enMisc from '../locales/en/misc'
import zhAdminAccounts from '../locales/zh/admin/accounts'
import zhAdminChannels from '../locales/zh/admin/channels'
import zhAdminOps from '../locales/zh/admin/ops'
import zhAdminOverview from '../locales/zh/admin/overview'
import zhAdminPlusQuotaAutomation from '../locales/zh/admin/plusQuotaAutomation'
import zhAdminOneClickAccountNotes from '../locales/zh/admin/oneClickAccountNotes'
import zhAdminResources from '../locales/zh/admin/resources'
import zhAdminSettings from '../locales/zh/admin/settings'
import zhCommon from '../locales/zh/common'
import zhDashboard from '../locales/zh/dashboard'
import zhLanding from '../locales/zh/landing'
import zhMisc from '../locales/zh/misc'

// locales/{zh,en}/index.ts 与 admin/index.ts 使用对象展开聚合各域模块，
// 展开模块之间若出现同名顶层键会静默覆盖。本测试将该风险固化为显式失败。
type Modules = Record<string, Record<string, unknown>>

function collisions(modules: Modules): string[] {
  const seen = new Map<string, string>()
  const out: string[] = []
  for (const [name, mod] of Object.entries(modules)) {
    for (const key of Object.keys(mod)) {
      const prev = seen.get(key)
      if (prev) {
        out.push(`"${key}" in both ${prev} and ${name}`)
      } else {
        seen.set(key, name)
      }
    }
  }
  return out
}

const roots: Record<string, Modules> = {
  zh: { landing: zhLanding, common: zhCommon, dashboard: zhDashboard, misc: zhMisc },
  en: { landing: enLanding, common: enCommon, dashboard: enDashboard, misc: enMisc }
}

const admins: Record<string, Modules> = {
  zh: {
    overview: zhAdminOverview,
    channels: zhAdminChannels,
    accounts: zhAdminAccounts,
    plusQuotaAutomation: zhAdminPlusQuotaAutomation,
    oneClickAccountNotes: zhAdminOneClickAccountNotes,
    resources: zhAdminResources,
    ops: zhAdminOps,
    settings: zhAdminSettings
  },
  en: {
    overview: enAdminOverview,
    channels: enAdminChannels,
    accounts: enAdminAccounts,
    plusQuotaAutomation: enAdminPlusQuotaAutomation,
    oneClickAccountNotes: enAdminOneClickAccountNotes,
    resources: enAdminResources,
    ops: enAdminOps,
    settings: enAdminSettings
  }
}

const oneClickAccountNotesReasonCodes = [
  'ACCOUNT_NOTE_IMPORT_BUSY',
  'ACCOUNT_NOTE_IMPORT_FILE_EMPTY',
  'ACCOUNT_NOTE_IMPORT_FILE_INVALID',
  'ACCOUNT_NOTE_IMPORT_FILE_REQUIRED',
  'ACCOUNT_NOTE_IMPORT_FILE_TOO_LARGE',
  'ACCOUNT_NOTE_IMPORT_FILE_TYPE_INVALID',
  'ACCOUNT_NOTE_IMPORT_INVALID_BOM',
  'ACCOUNT_NOTE_IMPORT_INVALID_LINE_ENDING',
  'ACCOUNT_NOTE_IMPORT_INVALID_UTF8',
  'ACCOUNT_NOTE_IMPORT_LINE_TOO_LONG',
  'ACCOUNT_NOTE_IMPORT_MULTIPART_INVALID',
  'ACCOUNT_NOTE_IMPORT_MULTIPART_REQUIRED',
  'ACCOUNT_NOTE_IMPORT_NO_RECORDS',
  'ACCOUNT_NOTE_IMPORT_NOT_APPLICABLE',
  'ACCOUNT_NOTE_IMPORT_NUL_BYTE',
  'ACCOUNT_NOTE_IMPORT_PLAN_INVALID',
  'ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE',
  'ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_INVALID',
  'ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_REQUIRED',
  'ACCOUNT_NOTE_IMPORT_PREVIEW_STALE',
  'ACCOUNT_NOTE_IMPORT_REQUEST_TOO_LARGE',
  'ACCOUNT_NOTE_IMPORT_TOO_MANY_LINES',
  'ACCOUNT_NOTE_IMPORT_UNAVAILABLE',
  'ACCOUNT_NOTE_IMPORT_UPLOAD_TIMEOUT',
  'IDEMPOTENCY_EXECUTOR_NIL',
  'IDEMPOTENCY_IN_PROGRESS',
  'IDEMPOTENCY_KEY_CONFLICT',
  'IDEMPOTENCY_KEY_INVALID',
  'IDEMPOTENCY_KEY_REQUIRED',
  'IDEMPOTENCY_PAYLOAD_INVALID',
  'IDEMPOTENCY_RETRY_BACKOFF',
  'IDEMPOTENCY_SCOPE_REQUIRED',
  'IDEMPOTENCY_STORE_UNAVAILABLE'
] as const

describe.each(Object.keys(roots))('locale %s spread assembly', (locale) => {
  it('root modules have no overlapping top-level keys', () => {
    expect(collisions(roots[locale])).toEqual([])
  })

  it('root modules do not shadow the explicit "admin" namespace', () => {
    for (const [name, mod] of Object.entries(roots[locale])) {
      expect(Object.keys(mod), `module ${name} must not define "admin"`).not.toContain('admin')
    }
  })

  it('admin modules have no overlapping top-level keys', () => {
    expect(collisions(admins[locale])).toEqual([])
  })

  it('maps every stable one-click account notes backend reason code', () => {
    const feature = admins[locale].oneClickAccountNotes.oneClickAccountNotes as Record<string, unknown>
    const errors = feature.errors as Record<string, unknown>
    for (const code of oneClickAccountNotesReasonCodes) {
      expect(errors[code], `${locale} is missing ${code}`).toEqual(expect.any(String))
    }
  })
})
