import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

function messageEntries(node: unknown, path = ''): Array<[string, string]> {
  if (typeof node === 'string') {
    return [[path, node]]
  }

  if (!node || typeof node !== 'object' || Array.isArray(node)) {
    throw new TypeError(`${path || 'locale root'} must contain only message objects and strings`)
  }

  return Object.entries(node as Record<string, unknown>)
    .sort(([left], [right]) => left.localeCompare(right))
    .flatMap(([key, value]) => messageEntries(value, path ? `${path}.${key}` : key))
}

describe('one-click account notes locale assembly', () => {
  it.each([
    ['en', en],
    ['zh', zh]
  ] as const)('%s exposes the feature and navigation label from production roots', (_locale, root) => {
    expect(root.nav.oneClickAccountNotes.trim()).not.toBe('')

    const messages = messageEntries(root.admin.oneClickAccountNotes)
    expect(messages.length).toBeGreaterThan(0)
    expect(messages.every(([, message]) => message.trim().length > 0)).toBe(true)
  })

  it('keeps the English and Chinese feature message shapes aligned', () => {
    const enPaths = messageEntries(en.admin.oneClickAccountNotes).map(([path]) => path)
    const zhPaths = messageEntries(zh.admin.oneClickAccountNotes).map(([path]) => path)

    expect(enPaths).toEqual(zhPaths)
  })
})
