export type ModelTracingConfigSource = 'disabled' | 'deployment' | 'runtime'
export type ModelTracingDestination = 'langfuse' | 'otlp_collector'

export interface ModelTracingConfig {
  configured: boolean
  enabled: boolean
  destination: ModelTracingDestination
  endpoint: string
  public_key: string
  has_secret: boolean
  prompt_max_bytes: number
  response_max_bytes: number
  media_max_bytes: number
  capture_media_content: boolean
  source: ModelTracingConfigSource
  config_version: number
  updated_at?: string
  updated_by?: number
}

export interface UpdateModelTracingConfig {
  expected_config_version: number
  enabled: boolean
  destination: ModelTracingDestination
  endpoint: string
  public_key: string
  secret_key?: string
  prompt_max_bytes: number
  response_max_bytes: number
  media_max_bytes: number
  capture_media_content: boolean
}
