import { apiClient } from '@/api/client'

export interface CodexStateStateMeta {
  length: number
  proxy_id?: number
  acquired_at: string
  expires_at: string
  verified_at: string
}

export interface CodexStateMintRun {
  running: boolean
  attempts: number
  started_at?: string
  last_attempt_at?: string
  next_retry_at?: string
  last_error?: string
}

export interface CodexStateModelStatus {
  model: string
  degraded: boolean
  last_292_at?: string
  last_312_at?: string
  last_mint_at?: string
  current?: CodexStateStateMeta
  previous?: CodexStateStateMeta
  mint_run: CodexStateMintRun
}

export interface CodexStateAccount {
  id: number
  name: string
  status: string
  schedulable: boolean
  auto_mint: boolean
  concurrency: number
  refresh_before_minutes: number
  degraded: boolean
  normal_proxy_id?: number
  state_proxy_ids: number[]
  models: CodexStateModelStatus[]
}

export interface CodexStateProxy {
  id: number
  name: string
  protocol: string
  host: string
  port: number
  status: string
}

export interface CodexStateOverview {
  default_model: string
  accounts: CodexStateAccount[]
  proxies: CodexStateProxy[]
}

export interface UpdateCodexStateAccountRequest {
  auto_mint?: boolean
  concurrency?: number
  refresh_before_minutes?: number
  normal_proxy_id?: number
  state_proxy_ids?: number[]
}

export const codexStateAPI = {
  async getOverview(): Promise<CodexStateOverview> {
    const { data } = await apiClient.get<CodexStateOverview>('/admin/codex-state/overview')
    return data
  },

  async updateAccount(id: number, payload: UpdateCodexStateAccountRequest): Promise<CodexStateAccount> {
    const { data } = await apiClient.put<CodexStateAccount>(`/admin/codex-state/accounts/${id}`, payload)
    return data
  },

  async triggerMint(id: number, model: string): Promise<void> {
    await apiClient.post(`/admin/codex-state/accounts/${id}/mint`, { model })
  }
}

export default codexStateAPI
