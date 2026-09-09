import { Activity, Braces, Radio, Workflow } from "lucide-react"
import type { ReactNode } from "react"
import { Link, NavLink } from "react-router-dom"

interface AppShellProps {
  children: ReactNode
}

export function AppShell({ children }: AppShellProps) {
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Link className="brand" to="/">
          <span className="brand-mark">B</span>
          <span>
            <strong>Blix</strong>
            <small>control room</small>
          </span>
        </Link>

        <nav className="nav-list" aria-label="Main navigation">
          <NavLink className={({ isActive }) => `nav-item${isActive ? " active" : ""}`} to="/runs">
            <Workflow size={17} />
            Runs
          </NavLink>
        </nav>

        <div className="sidebar-spacer" />
        <div className="system-card">
          <div className="system-card-title">
            <Activity size={14} /> local stack
          </div>
          <div className="system-row"><Radio size={13} /> Runway <span className="healthy-dot" /></div>
          <div className="system-row"><Braces size={13} /> read-only console</div>
        </div>
        <div className="environment-label">LOCAL DEVELOPMENT</div>
      </aside>
      <main className="main-content">{children}</main>
    </div>
  )
}
