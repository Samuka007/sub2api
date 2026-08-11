import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import {
  applyOneClickAccountNotes,
  previewOneClickAccountNotes,
  type OneClickAccountNotesApplyResult,
  type OneClickAccountNotesPreview
} from '@/api/admin/accounts'

const preview: OneClickAccountNotesPreview = {
  preview_digest: 'preview-digest',
  total_lines: 2,
  valid_lines: 2,
  invalid_lines: 0,
  duplicate_lines: 0,
  conflict_lines: 0,
  matched_lines: 1,
  unmatched_lines: 1,
  matched_accounts: 1,
  will_update_accounts: 1,
  unchanged_accounts: 0,
  can_apply: true,
  entries: [{
    line_number: 1,
    email: 'a***@example.com',
    status: 'matched',
    matched_accounts: 1,
    will_update_accounts: 1
  }]
}

const applied: OneClickAccountNotesApplyResult = {
  matched_lines: 1,
  matched_accounts: 1,
  updated_accounts: 1,
  unchanged_accounts: 0,
  unmatched_lines: 1,
  invalid_lines: 0,
  conflict_lines: 0
}

describe('admin one-click account notes API', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('uploads the selected TXT file for preview and forwards cancellation', async () => {
    const file = new File(['sensitive original line'], 'notes.txt', { type: 'text/plain' })
    const controller = new AbortController()
    post.mockResolvedValueOnce({ data: preview })

    await expect(previewOneClickAccountNotes(file, { signal: controller.signal })).resolves.toBe(preview)

    const [path, body, config] = post.mock.calls[0]
    expect(path).toBe('/admin/accounts/one-click-notes/preview')
    expect(body).toBeInstanceOf(FormData)
    expect(body.get('file')).toBe(file)
    expect(body.get('preview_digest')).toBeNull()
    expect(config).toEqual({
      headers: { 'Content-Type': 'multipart/form-data' },
      signal: controller.signal
    })
  })

  it('uploads the same file and preview digest when applying', async () => {
    const file = new File(['sensitive original line'], 'notes.txt', { type: 'text/plain' })
    const controller = new AbortController()
    post.mockResolvedValueOnce({ data: applied })

    await expect(
      applyOneClickAccountNotes(file, 'preview-digest', 'operation-key', { signal: controller.signal })
    ).resolves.toBe(applied)

    const [path, body, config] = post.mock.calls[0]
    expect(path).toBe('/admin/accounts/one-click-notes/apply')
    expect(body).toBeInstanceOf(FormData)
    expect(body.get('file')).toBe(file)
    expect(body.get('preview_digest')).toBe('preview-digest')
    expect(config).toEqual({
      headers: {
        'Content-Type': 'multipart/form-data',
        'Idempotency-Key': 'operation-key'
      },
      signal: controller.signal
    })
  })
})
