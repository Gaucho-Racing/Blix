import { ArrowRight, GitPullRequest } from "lucide-react"
import { Link } from "react-router-dom"
import { StatusBadge } from "@/components/status-badge"
import { formatTimestamp } from "@/lib/format"
import type { WikiChangeset } from "@/lib/knowledge"

export function DocumentChangesets({
  changesets,
}: {
  changesets: WikiChangeset[]
}) {
  const open = changesets.filter(
    (changeset) => !["committed", "rejected"].includes(changeset.status),
  )

  if (!open.length) return null

  return (
    <section className="panel document-open-changesets">
      <header>
        <h2>Open changesets</h2>
        <span>{open.length}</span>
      </header>
      <ul>
        {open.map((changeset) => (
          <li key={changeset.changeset_id}>
            <Link
              className="document-changeset-row"
              to={`/knowledge/proposals/${changeset.changeset_id}`}
            >
              <GitPullRequest size={18} aria-hidden="true" />
              <div className="document-changeset-copy">
                <strong>{changeset.summary}</strong>
                <p>
                  {changeset.season_slug || "Season-independent"}
                  <span>·</span>
                  {changeset.type}
                  <span>·</span>
                  {formatTimestamp(changeset.created_at)}
                </p>
              </div>
              <StatusBadge status={changeset.status} />
              <ArrowRight size={16} aria-hidden="true" />
            </Link>
          </li>
        ))}
      </ul>
    </section>
  )
}
