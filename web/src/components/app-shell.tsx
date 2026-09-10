import { Bot, BookOpen, CalendarClock, Gauge, LockKeyhole, LogIn, LogOut, MessageSquare, ServerCog, Workflow, Wrench } from "lucide-react"
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom"

import { useAuth } from "@/lib/auth"

const navClassName = ({ isActive }: { isActive: boolean }) => `nav-item${isActive ? " active" : ""}`

export function AppShell() {
  const auth = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const displayName = auth.user ? `${auth.user.first_name} ${auth.user.last_name}`.trim() || auth.user.username : "Sign in"
  const initials = auth.user ? `${auth.user.first_name?.[0] ?? ""}${auth.user.last_name?.[0] ?? ""}` || "GR" : "GR"
  const accountAction = () => {
    if (auth.user) {
      auth.logout()
      return
    }
    navigate("/auth/login", { state: { from: location } })
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Link className="brand" to="/chat">
          <span className="brand-mark">B</span>
          <span>
            <strong>Blix</strong>
            <small>Gaucho Racing AI</small>
          </span>
        </Link>

        <nav className="sidebar-nav" aria-label="Main navigation">
          <section className="nav-section">
            <div className="nav-label">Workspace</div>
            <div className="nav-list">
              <NavLink className={navClassName} to="/chat">
                <MessageSquare size={17} />
                Chat
              </NavLink>
              <NavLink className={navClassName} to="/knowledge">
                <BookOpen size={17} />
                Knowledge
              </NavLink>
              <NavLink className={navClassName} to="/automations">
                <CalendarClock size={17} />
                Automations
              </NavLink>
            </div>
          </section>

          <section className="nav-section admin-navigation">
            <div className="nav-label">Admin</div>
            <div className="nav-list">
              <NavLink className={(state) => `${navClassName(state)}${auth.isAdmin ? "" : " locked"}`} end to="/admin">
                <Gauge size={17} />
                Overview
                {!auth.isAdmin && <LockKeyhole className="nav-lock" size={13} />}
              </NavLink>
              <NavLink className={(state) => `${navClassName(state)}${auth.isAdmin ? "" : " locked"}`} to="/admin/runs">
                <Workflow size={17} />
                Runs
                {!auth.isAdmin && <LockKeyhole className="nav-lock" size={13} />}
              </NavLink>
              <NavLink className={(state) => `${navClassName(state)}${auth.isAdmin ? "" : " locked"}`} to="/admin/workers">
                <ServerCog size={17} />
                Workers
                {!auth.isAdmin && <LockKeyhole className="nav-lock" size={13} />}
              </NavLink>
              <NavLink className={(state) => `${navClassName(state)}${auth.isAdmin ? "" : " locked"}`} to="/admin/agents">
                <Bot size={17} />
                Agents
                {!auth.isAdmin && <LockKeyhole className="nav-lock" size={13} />}
              </NavLink>
              <NavLink className={(state) => `${navClassName(state)}${auth.isAdmin ? "" : " locked"}`} to="/admin/tools">
                <Wrench size={17} />
                Tools
                {!auth.isAdmin && <LockKeyhole className="nav-lock" size={13} />}
              </NavLink>
            </div>
          </section>
        </nav>

        <div className="sidebar-spacer" />
        <div className="runtime-status">
          <span className="healthy-dot" /> Runway connected
        </div>
        <button className="workspace-switcher" onClick={accountAction} type="button">
          {auth.user?.avatar_url ? <img alt="" className="workspace-avatar workspace-avatar-image" src={auth.user.avatar_url} /> : <span className="workspace-avatar">{initials.toUpperCase()}</span>}
          <span className="workspace-copy">
            <strong>{displayName}</strong>
            <small>{auth.user ? auth.user.email : "Continue with Sentinel"}</small>
          </span>
          {auth.user ? <LogOut size={15} /> : <LogIn size={15} />}
        </button>
      </aside>
      <main className="main-content"><Outlet /></main>
    </div>
  )
}
