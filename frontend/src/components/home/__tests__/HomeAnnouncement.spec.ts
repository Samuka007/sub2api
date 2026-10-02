import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import HomeAnnouncement from '../HomeAnnouncement.vue'
import announcementsAPI from '@/api/announcements'

vi.mock('@/api/announcements', () => ({
  default: {
    listPublic: vi.fn(),
  },
}))

const announcement = {
  id: 42,
  title: 'Service update',
  content:
    'A longer announcement explaining the maintenance window and what users should expect while the service is being updated.',
}

describe('HomeAnnouncement', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.mocked(announcementsAPI.listPublic).mockReset()
  })

  it('shows the newest public announcement and enlarges long content on demand', async () => {
    vi.mocked(announcementsAPI.listPublic).mockResolvedValue([announcement])
    const wrapper = mount(HomeAnnouncement, { props: { isChinese: false } })
    await flushPromises()

    expect(wrapper.get('[data-testid="home-announcement"]').text()).toContain('Service update')
    const details = wrapper.get('[data-testid="home-announcement-details"]')
    expect(details.attributes('aria-expanded')).toBe('false')

    await details.trigger('click')
    expect(details.attributes('aria-expanded')).toBe('true')
    expect(details.text()).toContain('Reduce')
    expect(wrapper.get('[data-testid="home-announcement-panel"]').attributes('role')).toBe('dialog')

    await wrapper.get('[data-testid="home-announcement-backdrop"]').trigger('click')
    expect(details.attributes('aria-expanded')).toBe('false')
  })

  it('closes for the current page and shows again after a refresh-like remount', async () => {
    vi.mocked(announcementsAPI.listPublic).mockResolvedValue([announcement])
    const wrapper = mount(HomeAnnouncement, { props: { isChinese: true } })
    await flushPromises()

    await wrapper.get('[data-testid="home-announcement-dismiss"]').trigger('click')

    expect(wrapper.find('[data-testid="home-announcement"]').exists()).toBe(false)
    expect(localStorage.getItem('scibuddy:dismissed-home-announcements')).toBeNull()

    wrapper.unmount()
    const refreshedWrapper = mount(HomeAnnouncement, { props: { isChinese: true } })
    await flushPromises()
    expect(refreshedWrapper.find('[data-testid="home-announcement"]').exists()).toBe(true)
  })

  it('renders announcement markup as plain text', async () => {
    vi.mocked(announcementsAPI.listPublic).mockResolvedValue([
      { ...announcement, content: '<img src=x onerror="alert(1)"> Important update' },
    ])
    const wrapper = mount(HomeAnnouncement, { props: { isChinese: false } })
    await flushPromises()

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('[data-testid="home-announcement"]').text()).toContain('<img src=x')
  })

  it('keeps the homepage clear when the announcement request fails', async () => {
    vi.mocked(announcementsAPI.listPublic).mockRejectedValueOnce(new Error('offline'))
    const failedWrapper = mount(HomeAnnouncement, { props: { isChinese: false } })
    await flushPromises()
    expect(failedWrapper.find('[data-testid="home-announcement"]').exists()).toBe(false)
  })

  it('shows the preview fallback while the public announcement request is pending', async () => {
    vi.mocked(announcementsAPI.listPublic).mockReturnValueOnce(new Promise(() => undefined))
    const wrapper = mount(HomeAnnouncement, {
      props: {
        isChinese: true,
        fallbackAnnouncement: {
          id: -1,
          title: '公告预览',
          content: '刷新页面后再次显示。',
        },
      },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.get('[data-testid="home-announcement"]').text()).toContain('公告预览')
  })
})
