import { BookOpen, ChevronRight, Database, FileText, Hash, MessageSquareText, Plus, RefreshCw, Search } from "lucide-react"

const sources = [
  { detail: "642 pages", icon: BookOpen, name: "Engineering wiki", status: "Synced 8 min ago" },
  { detail: "18,420 messages", icon: MessageSquareText, name: "Discord archive", status: "Live" },
  { detail: "318 files", icon: FileText, name: "Team drive", status: "Synced 22 min ago" },
  { detail: "12 repositories", icon: Database, name: "Code repositories", status: "Synced 4 min ago" },
]

const recentItems = [
  { area: "Powertrain", source: "Wiki", title: "Accumulator architecture", updated: "12 min ago" },
  { area: "#electrical", source: "Discord", title: "CAN termination debugging thread", updated: "48 min ago" },
  { area: "Rules", source: "Research", title: "2026 EV rules comparison", updated: "Yesterday" },
  { area: "Chassis", source: "Drive", title: "Monocoque manufacturing plan", updated: "Sep 6" },
]

export function KnowledgePage() {
  return (
    <div className="workspace-page knowledge-page">
      <header className="workspace-toolbar">
        <div><span className="workspace-kicker">KNOWLEDGE</span><h1>Everything the team knows</h1><p>Search across docs, conversations, files, and code.</p></div>
        <button className="primary-button" type="button"><Plus size={16} /> Add source</button>
      </header>

      <label className="knowledge-search">
        <Search size={20} />
        <input placeholder="Search Blix knowledge…" type="search" />
        <kbd>⌘ K</kbd>
      </label>

      <section className="knowledge-section">
        <div className="content-heading"><div><h2>Sources</h2><span>4 connected</span></div><button type="button"><RefreshCw size={14} /> Sync all</button></div>
        <div className="source-grid">
          {sources.map(({ detail, icon: Icon, name, status }) => (
            <article className="source-card" key={name}>
              <div className="source-icon"><Icon size={19} /></div>
              <div><h3>{name}</h3><p>{detail}</p><span><i /> {status}</span></div>
              <ChevronRight size={16} />
            </article>
          ))}
        </div>
      </section>

      <section className="knowledge-section recent-knowledge">
        <div className="content-heading"><div><h2>Recently updated</h2><span>Across all sources</span></div><button type="button">View all</button></div>
        <div className="knowledge-list">
          {recentItems.map((item) => (
            <button className="knowledge-row" key={item.title} type="button">
              <span className="knowledge-type"><Hash size={15} /></span>
              <span className="knowledge-title"><strong>{item.title}</strong><small>{item.area}</small></span>
              <span className="source-pill">{item.source}</span>
              <time>{item.updated}</time>
              <ChevronRight size={15} />
            </button>
          ))}
        </div>
      </section>
    </div>
  )
}
