import axios, { AxiosError, type InternalAxiosRequestConfig } from "axios"

import { clearSession, loadSession, saveSession, type TokenResponse } from "@/lib/session"
import type {
  ExecutionDetails,
  ExecutionEvent,
  IdentitySummary,
  AppendedMessage,
  CreatedRun,
  RunDetails,
  RunSummary,
  WorkerSummary,
} from "@/lib/types"

export const api = axios.create({
  baseURL: "/api",
  timeout: 10_000,
})

type RetriedRequest = InternalAxiosRequestConfig & { _retried?: boolean }

api.interceptors.request.use((config) => {
  const session = loadSession()
  if (session?.accessToken) config.headers.set("Authorization", `Bearer ${session.accessToken}`)
  return config
})

let refreshing: Promise<string | null> | null = null

export function refreshAccessToken(): Promise<string | null> {
  if (refreshing) return refreshing
  const session = loadSession()
  if (!session?.refreshToken) return Promise.resolve(null)
  refreshing = axios
    .post<TokenResponse>("/api/v1/auth/refresh", { refresh_token: session.refreshToken })
    .then(({ data }) => {
      saveSession({
        accessToken: data.access_token,
        refreshToken: data.refresh_token || session.refreshToken,
        expiresIn: data.expires_in,
      })
      return data.access_token
    })
    .catch(() => {
      clearSession()
      return null
    })
    .finally(() => {
      refreshing = null
    })
  return refreshing
}

api.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const request = error.config as RetriedRequest | undefined
    const path = request?.url ?? ""
    const isAuthExchange = path.includes("/auth/login") || path.includes("/auth/refresh")
    if (error.response?.status !== 401 || !request || request._retried || isAuthExchange) {
      return Promise.reject(error)
    }
    request._retried = true
    const accessToken = await refreshAccessToken()
    if (!accessToken) {
      redirectToLogin()
      return Promise.reject(error)
    }
    request.headers.set("Authorization", `Bearer ${accessToken}`)
    return api(request)
  },
)

function redirectToLogin() {
  const current = `${window.location.pathname}${window.location.search}${window.location.hash}`
  const returnTo = current.startsWith("/auth/") ? "/chat" : current
  window.location.assign(`/auth/login?return_to=${encodeURIComponent(returnTo)}`)
}

export async function listRuns(): Promise<RunSummary[]> {
  const { data } = await api.get<{ runs: RunSummary[] }>("/v1/runs", {
    params: { limit: 100 },
  })
  return data.runs
}

export async function listMyRuns(): Promise<RunSummary[]> {
  const { data } = await api.get<{ runs: RunSummary[] }>("/v1/runs", {
    params: { limit: 50, owner: "me" },
  })
  return data.runs
}

export async function createRun(): Promise<CreatedRun> {
  const { data } = await api.post<CreatedRun>("/v1/runs", {
    agent_id: "general_conversation_v1",
  })
  return data
}

export async function appendRunMessage(runID: string, content: string): Promise<AppendedMessage> {
  const { data } = await api.post<AppendedMessage>(`/v1/runs/${runID}/messages`, { content })
  return data
}

export async function listWorkers(): Promise<WorkerSummary[]> {
  const { data } = await api.get<{ workers: WorkerSummary[] }>("/v1/workers", {
    params: { limit: 100 },
  })
  return data.workers
}

export async function getRun(runID: string): Promise<RunDetails> {
  const { data } = await api.get<RunDetails>(`/v1/runs/${runID}`)
  return data
}

export async function getExecution(executionID: string): Promise<ExecutionDetails> {
  const { data } = await api.get<ExecutionDetails>(`/v1/executions/${executionID}`)
  return data
}

export async function getRunEvents(runID: string): Promise<ExecutionEvent[]> {
  const events: ExecutionEvent[] = []
  let after = 0

  while (true) {
    const { data } = await api.get<{ events: ExecutionEvent[] }>(`/v1/runs/${runID}/events`, {
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
  const { data } = await api.post<IdentitySummary[]>("/identities/resolve", { ids: entityIDs })
  return data
}
