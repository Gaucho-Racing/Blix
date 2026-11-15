export type ExecutionStatus =
  | "idle"
  | "queued"
  | "running"
  | "waiting"
  | "succeeded"
  | "failed"
  | "cancelled"

export interface IdentityApplicationSummary {
  id: string
  name: string
  client_id: string
  icon_url: string
}

export interface IdentitySummary {
  id: string
  type: string
  name: string
  username?: string
  avatar_url?: string
  application?: IdentityApplicationSummary
}

export type WorkerStatus = "offline" | "idle" | "active" | "saturated"

export interface WorkerSummary {
  worker_id: string
  status: WorkerStatus
  concurrency: number
  active_executions: number
  available_concurrency: number
  executions_claimed: number
  executions_succeeded: number
  executions_failed: number
  executions_cancelled: number
  model_calls: number
  tool_calls: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
  registered_at: string
  last_seen_at: string
  heartbeat_expires_at: string
}
