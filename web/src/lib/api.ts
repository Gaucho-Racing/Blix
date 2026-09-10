import axios from "axios"

import type {
  ExecutionDetails,
  ExecutionEvent,
  IdentitySummary,
  RunDetails,
  RunSummary,
  WorkerSummary,
} from "@/lib/types"

const api = axios.create({
  baseURL: "/api/v1",
  timeout: 10_000,
})

export async function listRuns(): Promise<RunSummary[]> {
  const { data } = await api.get<{ runs: RunSummary[] }>("/runs", {
    params: { limit: 100 },
  })
  return data.runs
}

export async function listWorkers(): Promise<WorkerSummary[]> {
  const { data } = await api.get<{ workers: WorkerSummary[] }>("/workers", {
    params: { limit: 100 },
  })
  return data.workers
}

export async function getRun(runID: string): Promise<RunDetails> {
  const { data } = await api.get<RunDetails>(`/runs/${runID}`)
  return data
}

export async function getExecution(executionID: string): Promise<ExecutionDetails> {
  const { data } = await api.get<ExecutionDetails>(`/executions/${executionID}`)
  return data
}

export async function getRunEvents(runID: string): Promise<ExecutionEvent[]> {
  const events: ExecutionEvent[] = []
  let after = 0

  while (true) {
    const { data } = await api.get<{ events: ExecutionEvent[] }>(`/runs/${runID}/events`, {
      params: { after },
    })
    events.push(...data.events)
    if (data.events.length < 100) {
      return events
    }
    after = data.events[data.events.length - 1].position
  }
}

export async function resolveIdentities(entityIDs: string[]): Promise<IdentitySummary[]> {
  if (entityIDs.length === 0) return []
  const { data } = await axios.post<IdentitySummary[]>("/api/identities/resolve", { ids: entityIDs })
  return data
}
