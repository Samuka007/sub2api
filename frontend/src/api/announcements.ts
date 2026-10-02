/**
 * User Announcements API endpoints
 */

import { apiClient } from './client'
import type { PublicAnnouncement, UserAnnouncement } from '@/types'

export async function listPublic(): Promise<PublicAnnouncement[]> {
  const { data } = await apiClient.get<PublicAnnouncement[]>('/public/announcements')
  return data
}

export async function list(unreadOnly: boolean = false): Promise<UserAnnouncement[]> {
  const { data } = await apiClient.get<UserAnnouncement[]>('/announcements', {
    params: unreadOnly ? { unread_only: 1 } : {}
  })
  return data
}

export async function markRead(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>(`/announcements/${id}/read`)
  return data
}

const announcementsAPI = {
  listPublic,
  list,
  markRead
}

export default announcementsAPI
