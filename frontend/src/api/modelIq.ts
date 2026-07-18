import { apiClient } from './client'

export interface ModelIqDayResult {
  date: string
  score: number
  status: string
  passed: number
  tasks: number
  invalid: number
  total_tokens: number
  input_tokens: number
  cached_input_tokens: number
  output_tokens: number
  wall_seconds: number
  wall_time_human: string
}

export interface ModelIqLatestResult extends ModelIqDayResult {
  model: string
  reasoning_effort: string
  valid_tasks: number
  cost_usd: number
}

export interface ModelIqComparison {
  label: string
  model: string
  reasoning_effort: string
  latest: ModelIqLatestResult
  recent_days: ModelIqDayResult[]
}

export interface ModelIqData {
  comparisons: Record<string, ModelIqComparison>
}

export interface ModelIqCurrentResponse {
  model_iq: ModelIqData
  fetched_at: string
  monitored_at?: string
  status?: string
  stale: boolean
}

export async function getCurrentModelIq(options?: {
  signal?: AbortSignal
}): Promise<ModelIqCurrentResponse> {
  const { data } = await apiClient.get<ModelIqCurrentResponse>('/model-iq', {
    signal: options?.signal,
  })
  return data
}

export const modelIqAPI = {
  getCurrent: getCurrentModelIq,
}

export default modelIqAPI
