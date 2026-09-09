import type { LucideIcon } from "lucide-react"

interface PlaceholderPageProps {
  description: string
  eyebrow: string
  icon: LucideIcon
  label: string
  title: string
}

export function PlaceholderPage({ description, eyebrow, icon: Icon, label, title }: PlaceholderPageProps) {
  return (
    <div className="page placeholder-page">
      <header className="page-header">
        <div>
          <div className="eyebrow">{eyebrow}</div>
          <h1>{title}</h1>
          <p>{description}</p>
        </div>
      </header>

      <section className="panel placeholder-panel">
        <div className="placeholder-icon"><Icon size={24} /></div>
        <div>
          <span className="placeholder-label">{label}</span>
          <h2>This workspace is ready to build.</h2>
          <p>We’ll connect the interface to Runway as the product flow takes shape.</p>
        </div>
      </section>
    </div>
  )
}
