import { ArrowRight, LoaderCircle, ShieldCheck } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { useLocation, useNavigate, useSearchParams } from "react-router-dom"

import { api } from "@/lib/api"
import { clearSession, saveSession, type TokenResponse } from "@/lib/session"

const oauthStateKey = "blix_oauth_state"
const oauthReturnToKey = "blix_oauth_return_to"

interface AuthConfiguration {
  authorization_url: string
}

export function LoginPage() {
  const location = useLocation()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const started = useRef(false)
  const [error, setError] = useState(() => searchParams.get("error_description") || searchParams.get("error") || "")
  const fromLocation = (location.state as { from?: { pathname: string; search?: string; hash?: string } } | null)?.from
  const requestedReturnTo = searchParams.get("return_to")
    || (fromLocation ? `${fromLocation.pathname}${fromLocation.search ?? ""}${fromLocation.hash ?? ""}` : "/chat")

  useEffect(() => {
    if (started.current) return
    started.current = true
    if (window.location.hostname === "127.0.0.1") {
      const canonical = new URL(window.location.href)
      canonical.hostname = "localhost"
      window.location.replace(canonical)
      return
    }
    const code = searchParams.get("code")
    const oauthError = searchParams.get("error")
    if (oauthError) return
    if (code) {
      void completeLogin(code, searchParams.get("state"), navigate, setError)
      return
    }
    void beginLogin(sanitizeReturnTo(requestedReturnTo)).catch((loginError: unknown) => {
      setError(errorMessage(loginError))
    })
  }, [navigate, requestedReturnTo, searchParams])

  return (
    <main className="login-page">
      <section className="login-card">
        <div className="login-mark">B</div>
        <span className="eyebrow">GAUCHO RACING</span>
        <h1>Sign in to Blix</h1>
        {error ? (
          <>
            <p>{error}</p>
            <button className="primary-button" onClick={() => window.location.assign(`/auth/login?return_to=${encodeURIComponent(sanitizeReturnTo(requestedReturnTo))}`)} type="button">
              Try again <ArrowRight size={15} />
            </button>
          </>
        ) : (
          <>
            <p>Connecting securely through Sentinel.</p>
            <div className="login-progress"><LoaderCircle className="spin" size={18} /> Redirecting to sign in</div>
          </>
        )}
        <div className="login-trust"><ShieldCheck size={14} /> Gaucho Racing identity</div>
      </section>
    </main>
  )
}

async function beginLogin(returnTo: string) {
  clearSession()
  const state = crypto.randomUUID()
  const redirectURI = oauthRedirectURI()
  sessionStorage.setItem(oauthStateKey, state)
  sessionStorage.setItem(oauthReturnToKey, returnTo)
  const { data } = await api.get<AuthConfiguration>("/v1/auth/config", {
    params: { state, redirect_uri: redirectURI },
  })
  window.location.assign(data.authorization_url)
}

async function completeLogin(
  code: string,
  state: string | null,
  navigate: ReturnType<typeof useNavigate>,
  setError: (message: string) => void,
) {
  const expectedState = sessionStorage.getItem(oauthStateKey)
  if (!state || !expectedState || state !== expectedState) {
    setError("The Sentinel login state did not match. Please start again.")
    return
  }
  try {
    const { data } = await api.post<TokenResponse>("/v1/auth/login", {
      code,
      redirect_uri: oauthRedirectURI(),
    })
    saveSession({
      accessToken: data.access_token,
      refreshToken: data.refresh_token,
      expiresIn: data.expires_in,
    })
    const returnTo = sanitizeReturnTo(sessionStorage.getItem(oauthReturnToKey))
    sessionStorage.removeItem(oauthStateKey)
    sessionStorage.removeItem(oauthReturnToKey)
    navigate(returnTo, { replace: true })
  } catch (loginError) {
    clearSession()
    setError(errorMessage(loginError))
  }
}

function oauthRedirectURI(): string {
  return `${window.location.origin}/auth/login`
}

function sanitizeReturnTo(value: string | null): string {
  if (!value || !value.startsWith("/") || value.startsWith("//") || value.startsWith("/api/") || value.startsWith("/auth/")) {
    return "/chat"
  }
  return value
}

function errorMessage(error: unknown): string {
  const responseError = (error as { response?: { data?: { error?: string } } })?.response?.data?.error
  return responseError || "Sentinel sign-on failed. Please try again."
}
