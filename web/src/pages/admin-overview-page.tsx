import { Bot, ChevronRight, Workflow, Wrench } from "lucide-react"
import { Link } from "react-router-dom"

const destinations = [
  {
    description: "Trace messages, model calls, tools, and delegated executions.",
    href: "/admin/runs",
    icon: Workflow,
    label: "Runs",
    status: "Live",
  },
  {
    description: "Inspect agent definitions, capabilities, and availability.",
    href: "/admin/agents",
    icon: Bot,
    label: "Agents",
    status: "Placeholder",
  },
  {
    description: "Review registered tools and their runtime configuration.",
    href: "/admin/tools",
    icon: Wrench,
    label: "Tools",
    status: "Placeholder",
  },
]

export function AdminOverviewPage() {
  return (
    <div className="page admin-overview-page">
      <header className="page-header">
        <div>
          <div className="eyebrow">ADMIN</div>
          <h1>System overview</h1>
          <p>Inspect and manage the services that power Blix.</p>
        </div>
      </header>

      <section className="admin-destination-grid" aria-label="Admin views">
        {destinations.map(({ description, href, icon: Icon, label, status }) => (
          <Link className="admin-destination panel" key={href} to={href}>
            <div className="admin-destination-icon"><Icon size={19} /></div>
            <div className="admin-destination-copy">
              <div><h2>{label}</h2><span>{status}</span></div>
              <p>{description}</p>
            </div>
            <ChevronRight size={16} />
          </Link>
        ))}
      </section>
    </div>
  )
}
