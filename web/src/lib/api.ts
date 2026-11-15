import axios, { AxiosError, type InternalAxiosRequestConfig } from "axios"

import { clearSession, loadSession, saveSession, type TokenResponse } from "@/lib/session"
import type {
  IdentitySummary,
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

export async function listWorkers(): Promise<WorkerSummary[]> {
  const { data } = await api.get<{ workers: WorkerSummary[] }>("/v1/workers", {
    params: { limit: 100 },
  })
  return data.workers
}

export async function resolveIdentities(entityIDs: string[]): Promise<IdentitySummary[]> {
  if (entityIDs.length === 0) return []
  const { data } = await api.post<IdentitySummary[]>("/identities/resolve", { ids: entityIDs })
  return data
}
