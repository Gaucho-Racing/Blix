const sessionKey = "blix_web_session"

export interface TokenSession {
  accessToken: string
  refreshToken: string
  expiresIn: number
}

export interface TokenResponse {
  access_token: string
  refresh_token: string
  expires_in: number
}

export function saveSession(session: TokenSession) {
  localStorage.setItem(sessionKey, JSON.stringify(session))
}

export function loadSession(): TokenSession | null {
  const raw = localStorage.getItem(sessionKey)
  if (!raw) return null
  try {
    const session = JSON.parse(raw) as Partial<TokenSession>
    if (!session.accessToken || !session.refreshToken) {
      clearSession()
      return null
    }
    return {
      accessToken: session.accessToken,
      refreshToken: session.refreshToken,
      expiresIn: Number(session.expiresIn) || 0,
    }
  } catch {
    clearSession()
    return null
  }
}

export function clearSession() {
  localStorage.removeItem(sessionKey)
}
