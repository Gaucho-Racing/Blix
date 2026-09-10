import { useQuery, useQueryClient } from "@tanstack/react-query"

import { api } from "@/lib/api"
import { clearSession, loadSession } from "@/lib/session"

export interface AuthSession {
  entity_id: string
  entity_type: string
  user_id?: string
  service_account_id?: string
  scope: string
  groups: string[]
}

export interface CurrentUser {
  id: string
  entity_id: string
  username: string
  first_name: string
  last_name: string
  email: string
  phone_number: string
  gender: string
  birthday: string
  graduate_level: string
  graduation_year: number
  major: string
  shirt_size: string
  jacket_size: string
  sae_registration_number: string
  occupation_title: string
  occupation_company: string
  avatar_url: string
  initial_role: string
  groups: string[]
  updated_at: string
  created_at: string
}

export function useAuth() {
  const queryClient = useQueryClient()
  const tokenSession = loadSession()
  const sessionQuery = useQuery({
    queryKey: ["auth-session", tokenSession?.accessToken],
    queryFn: async () => (await api.get<AuthSession>("/v1/auth/session")).data,
    enabled: Boolean(tokenSession?.accessToken),
    retry: false,
    staleTime: 5 * 60 * 1000,
  })
  const userQuery = useQuery({
    queryKey: ["current-user", tokenSession?.accessToken],
    queryFn: async () => (await api.get<CurrentUser>("/auth/users/@me")).data,
    enabled: Boolean(tokenSession?.accessToken),
    retry: false,
    staleTime: 5 * 60 * 1000,
  })

  function logout() {
    void api.post("/v1/auth/logout").finally(() => {
      clearSession()
      queryClient.clear()
      window.location.assign("/auth/login")
    })
  }

  return {
    tokenSession,
    session: sessionQuery.data,
    user: userQuery.data,
    isAuthenticated: Boolean(tokenSession && sessionQuery.data && userQuery.data),
    isAdmin: userQuery.data?.groups.includes("BlixAdmins") ?? false,
    isLoading: Boolean(tokenSession) && (sessionQuery.isPending || userQuery.isPending),
    error: sessionQuery.error ?? userQuery.error,
    logout,
  }
}
