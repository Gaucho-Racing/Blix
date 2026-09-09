import { BellRing, CalendarClock, CheckCircle2, Clock3, GitBranch, MoreHorizontal, Plus, Repeat2, Zap } from "lucide-react"

const automations = [
  { icon: CalendarClock, name: "Competition deadline digest", schedule: "Every Monday at 9:00 AM", status: "Active", target: "#general" },
  { icon: BellRing, name: "Parts request follow-up", schedule: "Weekdays at 4:00 PM", status: "Active", target: "#operations" },
  { icon: Repeat2, name: "Weekly engineering summary", schedule: "Fridays at 5:00 PM", status: "Active", target: "Team leads" },
  { icon: GitBranch, name: "PR review roundup", schedule: "When a pull request is ready", status: "Draft", target: "Code owners" },
]

export function AutomationsPage() {
  return (
    <div className="workspace-page automations-page">
      <header className="workspace-toolbar">
        <div><span className="workspace-kicker">AUTOMATIONS</span><h1>Keep the team moving</h1><p>Schedule recurring work and follow autonomous runs from one place.</p></div>
        <button className="primary-button" type="button"><Plus size={16} /> New automation</button>
      </header>

      <section className="automation-stats" aria-label="Automation summary">
        <article><span className="metric-icon pink"><Zap size={17} /></span><div><strong>3</strong><span>Active</span></div></article>
        <article><span className="metric-icon green"><CheckCircle2 size={17} /></span><div><strong>12</strong><span>Runs this week</span></div></article>
        <article><span className="metric-icon blue"><Clock3 size={17} /></span><div><strong>8.4h</strong><span>Team time saved</span></div></article>
      </section>

      <section className="automation-panel">
        <div className="content-heading"><div><h2>Automations</h2><span>Scheduled and event-driven work</span></div><button type="button">View runbooks</button></div>
        <div className="automation-list">
          {automations.map(({ icon: Icon, name, schedule, status, target }) => (
            <article className="automation-row" key={name}>
              <div className="automation-icon"><Icon size={18} /></div>
              <div className="automation-copy"><h3>{name}</h3><p>{schedule}</p></div>
              <span className="automation-target">{target}</span>
              <span className={`automation-status ${status.toLowerCase()}`}><i /> {status}</span>
              <button aria-label={`More options for ${name}`} className="row-menu" type="button"><MoreHorizontal size={18} /></button>
            </article>
          ))}
        </div>
      </section>
    </div>
  )
}
