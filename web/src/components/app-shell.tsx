import { Activity, Bot, BookOpen, Braces, CalendarClock, Gauge, MessageSquare, Radio, Workflow, Wrench } from "lucide-react"
import { Link, NavLink, Outlet } from "react-router-dom"

const navClassName = ({ isActive }: { isActive: boolean }) => `nav-item${isActive ? " active" : ""}`

export function AppShell() {
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Link className="brand" to="/chat">
          <span className="brand-mark">B</span>
          <span>
            <strong>Blix</strong>
            <small>internal agent</small>
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
              <NavLink className={navClassName} end to="/admin">
                <Gauge size={17} />
                Overview
              </NavLink>
              <NavLink className={navClassName} to="/admin/runs">
                <Workflow size={17} />
                Runs
              </NavLink>
              <NavLink className={navClassName} to="/admin/agents">
                <Bot size={17} />
                Agents
              </NavLink>
              <NavLink className={navClassName} to="/admin/tools">
                <Wrench size={17} />
                Tools
              </NavLink>
            </div>
          </section>
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
      <main className="main-content"><Outlet /></main>
    </div>
  )
}
