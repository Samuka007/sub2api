import { apiClient } from './client'

export interface ModelRadarTable {
  headers: string[]
  rows: string[][]
}

export interface ModelRadarQualityCard {
  model: string
  score: string
  cost: string
  duration: string
}

export interface ModelRadarSection {
  title: string
  source_updated_at?: string
  summary?: string[]
  highlights?: string[]
  table?: ModelRadarTable
  cards?: ModelRadarQualityCard[]
}

export interface ModelRadarView {
  source_name: string
  source_url: string
  fetched_at: string
  stale: boolean
  refresh_allowed_at: string
  quota: ModelRadarSection
  fast: ModelRadarSection
  quality: ModelRadarSection
}

export async function getModelRadar(): Promise<ModelRadarView> {
  const { data } = await apiClient.get<ModelRadarView>('/admin/model-radar')
  return data
}

export async function refreshModelRadar(): Promise<ModelRadarView> {
  const { data } = await apiClient.post<ModelRadarView>('/admin/model-radar/refresh')
  return data
}
