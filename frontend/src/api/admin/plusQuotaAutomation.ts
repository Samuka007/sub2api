import { apiClient } from '../client'

const AUTOMATION_PATH = '/admin/openai/plus-quota-automation'
const ANOMALIES_PATH = '/admin/openai/plus-quota-anomalies'

export interface PlusQuotaAutomationConfig {
  enabled: boolean
  group_id: number
  interval_seconds: number
  utilization_threshold: number
}

export interface PlusQuotaAutomationState {
  running: boolean
  trigger: string
  started_at?: string
  completed_at?: string
  scanned: number
  eligible: number
  at_limit: number
  reset_count: number
  unauthorized: number
  failed: number
  no_credits: number
  cooldown: number
  skipped: number
  last_error?: string
  skipped_reason?: string
}

export interface PlusQuotaAutomationOverview {
  config: PlusQuotaAutomationConfig
  state: PlusQuotaAutomationState
  next_run_at?: string
}

export type PlusQuotaAnomalyStatus = 'open' | 'resolved'
export type PlusQuotaAnomalyStatusFilter = PlusQuotaAnomalyStatus | 'all'

export interface PlusQuotaAnomaly {
  account_id: number
  account_name: string
  email: string
  group_id: number
  stage: string
  http_status: number
  first_detected_at: string
  last_detected_at: string
  count: number
  status: PlusQuotaAnomalyStatus
  last_error: string
  resolved_at?: string
}

export interface ListPlusQuotaAnomaliesParams {
  page?: number
  page_size?: number
  status?: PlusQuotaAnomalyStatusFilter
  search?: string
}

export interface PlusQuotaAnomaliesResponse {
  items: PlusQuotaAnomaly[]
  total: number
  page: number
  page_size: number
}

export interface PlusQuotaAnomalyNotesExport {
  blob: Blob | null
  count: number
  filename: string | null
}

export async function getAutomation(): Promise<PlusQuotaAutomationOverview> {
  const { data } = await apiClient.get<PlusQuotaAutomationOverview>(AUTOMATION_PATH)
  return data
}

export async function updateAutomation(config: PlusQuotaAutomationConfig): Promise<void> {
  await apiClient.put(AUTOMATION_PATH, config)
}

export async function runAutomation(): Promise<PlusQuotaAutomationOverview> {
  const { data } = await apiClient.post<PlusQuotaAutomationOverview>(`${AUTOMATION_PATH}/run`)
  return data
}

export async function listAnomalies(
  params: ListPlusQuotaAnomaliesParams,
  options?: { signal?: AbortSignal }
): Promise<PlusQuotaAnomaliesResponse> {
  const { data } = await apiClient.get<PlusQuotaAnomaliesResponse>(ANOMALIES_PATH, {
    params,
    signal: options?.signal
  })
  return data
}

export async function exportAnomalyNotes(
  options?: { signal?: AbortSignal }
): Promise<PlusQuotaAnomalyNotesExport> {
  const response = await apiClient.get<Blob>(`${ANOMALIES_PATH}/export-notes`, {
    responseType: 'blob',
    signal: options?.signal
  })
  if (response.status === 204) {
    return { blob: null, count: 0, filename: null }
  }

  const disposition = response.headers?.['content-disposition']
  const filenameMatch = typeof disposition === 'string'
    ? disposition.match(/filename="?([^";]+)"?/i)
    : null
  const count = Number.parseInt(String(response.headers?.['x-exported-count'] || ''), 10)
  return {
    blob: response.data,
    count: Number.isFinite(count) && count > 0 ? count : 0,
    filename: filenameMatch?.[1] || null
  }
}

export async function resolveAnomaly(accountId: number): Promise<void> {
  await apiClient.post(`${ANOMALIES_PATH}/${accountId}/resolve`)
}

export const plusQuotaAutomationAPI = {
  getAutomation,
  updateAutomation,
  runAutomation,
  listAnomalies,
  exportAnomalyNotes,
  resolveAnomaly
}

export default plusQuotaAutomationAPI
