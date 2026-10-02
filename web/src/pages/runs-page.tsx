import { useQuery } from "@tanstack/react-query"
import { Search } from "lucide-react"
import { useState } from "react"
import { Link } from "react-router-dom"
import { listAdminRuns, errorMessage } from "@/lib/runtime"
import { formatTimestamp } from "@/lib/format"

export function RunsPage() {
  const [search, setSearch] = useState("")
  const query = useQuery({
    queryKey: ["admin", "runs"],
    queryFn: listAdminRuns,
    refetchInterval: 5000,
  })
  const filtered = query.data?.filter((run) =>
    `${run.name} ${run.run_id}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  )
  return (
    <div className="page">
      <header className="page-header">
        <div>
          <div className="eyebrow">ADMIN · RUNTIME</div>
          <h1>Runs</h1>
          <p>
            Inspect durable runs, turns, delegated tasks, and activity.
          </p>
        </div>
      </header>
      <section className="panel">
        <div className="panel-toolbar">
          <h2>Recent runs</h2>
          <label className="search-box">
            <Search size={15} />
            <input
              aria-label="Search runs"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Search name or ID"
            />
          </label>
        </div>
        {query.isPending && <p className="loading-state">Loading…</p>}
        {query.error && (
          <p className="runtime-error" role="alert">
            {errorMessage(query.error)}
          </p>
        )}
        {filtered?.length === 0 && (
          <p className="empty-state">No matching runs.</p>
        )}
        <div className="run-table-wrap">
          <table className="run-table">
            <thead>
              <tr>
                <th>Run</th>
                <th>Status</th>
                <th>Last activity</th>
              </tr>
            </thead>
            <tbody>
              {filtered?.map((run) => (
                <tr key={run.run_id}>
                  <td>
                    <Link
                      className="run-link"
                      to={`/admin/runs/${run.run_id}`}
                    >
                      <strong>{run.name || "New run"}</strong>
                      <code>{run.run_id}</code>
                    </Link>
                  </td>
                  <td>{run.status}</td>
                  <td>{formatTimestamp(run.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  )
}
