import { LoaderCircle, LogIn, ShieldX } from "lucide-react"
import { Navigate, Outlet, useLocation } from "react-router-dom"

import { useAuth } from "@/lib/auth"

export function RequireAdmin() {
  const location = useLocation()
  const auth = useAuth()

  if (auth.isLoading) {
    return <div className="page centered"><div className="loading-state"><LoaderCircle className="spin" size={18} /> Verifying Sentinel access</div></div>
  }
  if (!auth.tokenSession) {
    return <Navigate replace state={{ from: location }} to="/auth/login" />
  }
  if (auth.error) {
    return (
      <AdminAccessScreen
        action="Sign in again"
        description="You must be a member of the BlixAdmins group to access this page."
        onAction={auth.logout}
        title="Unauthorized"
      />
    )
  }
  if (!auth.isAuthenticated) {
    return <div className="page centered"><div className="loading-state"><LoaderCircle className="spin" size={18} /> Loading identity</div></div>
  }
  if (!auth.isAdmin) {
    return (
      <AdminAccessScreen
        action="Sign in with another account"
        description="You must be a member of the BlixAdmins group to access this page."
        onAction={auth.logout}
        title="Unauthorized"
      />
    )
  }
  return <Outlet />
}

interface AdminAccessScreenProps {
  title: string
  description: string
  action: string
  onAction: () => void
}

function AdminAccessScreen({ title, description, action, onAction }: AdminAccessScreenProps) {
  return (
    <div className="page admin-access-page">
      <section className="panel admin-access-card">
        <div className="admin-access-icon"><ShieldX size={25} /></div>
        <span className="eyebrow">BLIX ADMIN</span>
        <h1>{title}</h1>
        <p>{description}</p>
        <button className="secondary-button" onClick={onAction} type="button"><LogIn size={15} /> {action}</button>
      </section>
    </div>
  )
}
