import { Bot, BookOpen, CalendarClock, ChevronUp, Gauge, MessageSquare, Workflow, Wrench } from "lucide-react"
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
        <div className="runtime-status">
          <span className="healthy-dot" /> Runway connected
        </div>
        <button className="workspace-switcher" type="button">
          <span className="workspace-avatar">GR</span>
          <span className="workspace-copy">
            <strong>Gaucho Racing</strong>
            <small>Local workspace</small>
          </span>
          <ChevronUp size={15} />
        </button>
      </aside>
      <main className="main-content"><Outlet /></main>
    </div>
  )
}
