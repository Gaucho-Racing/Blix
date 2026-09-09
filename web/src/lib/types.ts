export type ExecutionStatus =
  | "idle"
  | "queued"
  | "running"
  | "waiting"
  | "succeeded"
  | "failed"
  | "cancelled"

export interface RunSummary {
  run_id: string
  kind: string
  status: string
  root_execution_id: string
  agent_id: string
  execution_status: ExecutionStatus
  execution_count: number
  event_count: number
  last_event_position: number
  created_at: string
  last_activity_at: string
}

export interface ExecutionSummary {
  execution_id: string
  run_id: string
  parent_execution_id: string | null
  agent_id: string
  status: ExecutionStatus
  version: number
  claimed_by?: string
  claimed_at?: string
  lease_expires_at?: string
  waiting_on_execution_id?: string
  completed_at?: string
  created_at: string
  updated_at: string
}

export interface RunDetails {
  run: RunSummary
  executions: ExecutionSummary[]
}

export interface ExecutionDetails {
  execution: ExecutionSummary
  task?: unknown
  state?: unknown
  result?: unknown
  has_provider_continuation: boolean
}

export interface ExecutionEvent {
  position: number
  event_id: string
  run_id: string
  execution_id: string
  type: string
  entity_id: string
  payload: Record<string, unknown>
  created_at: string
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
  registered_at: string
  last_seen_at: string
  heartbeat_expires_at: string
}
