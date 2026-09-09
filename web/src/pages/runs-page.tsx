import { useQuery } from "@tanstack/react-query"
import { Activity, Clock3, Layers3, LoaderCircle, Search, TriangleAlert } from "lucide-react"
import { useMemo, useState } from "react"
import { Link } from "react-router-dom"

import { StatusBadge } from "@/components/status-badge"
import { listRuns } from "@/lib/api"
import { formatRelative, formatTimestamp } from "@/lib/format"
import type { RunSummary } from "@/lib/types"

const noRuns: RunSummary[] = []

export function RunsPage() {
  const [search, setSearch] = useState("")
  const query = useQuery({
    queryKey: ["runs"],
    queryFn: listRuns,
    refetchInterval: 2_000,
  })
  const runs = query.data ?? noRuns
  const filteredRuns = useMemo(() => {
    const needle = search.trim().toLowerCase()
    if (!needle) return runs
    return runs.filter((run) =>
      [run.run_id, run.agent_id, run.kind, run.execution_status].some((value) => value.toLowerCase().includes(needle)),
    )
  }, [runs, search])

  const active = runs.filter((run) => ["queued", "running", "waiting"].includes(run.execution_status)).length
  const failures = runs.filter((run) => run.execution_status === "failed").length
  const eventCount = runs.reduce((total, run) => total + run.event_count, 0)

  return (
    <div className="page">
      <header className="page-header">
        <div>
          <div className="eyebrow">OBSERVABILITY</div>
          <h1>Execution runs</h1>
          <p>Trace agent work from intake through every model, tool, and delegation checkpoint.</p>
        </div>
        <div className="live-indicator"><span /> LIVE · 2S</div>
      </header>

      <section className="stat-grid" aria-label="Run summary">
        <article className="stat-card"><Layers3 size={17} /><div><span>Total runs</span><strong>{runs.length}</strong></div></article>
        <article className="stat-card"><Activity size={17} /><div><span>In flight</span><strong>{active}</strong></div></article>
        <article className="stat-card"><Clock3 size={17} /><div><span>Events loaded</span><strong>{eventCount.toLocaleString()}</strong></div></article>
        <article className={`stat-card${failures ? " danger" : ""}`}><TriangleAlert size={17} /><div><span>Failed</span><strong>{failures}</strong></div></article>
      </section>

      <section className="panel runs-panel">
        <div className="panel-toolbar">
          <div>
            <h2>Recent activity</h2>
            <span>{filteredRuns.length} of {runs.length} runs</span>
          </div>
          <label className="search-box">
            <Search size={15} />
            <input onChange={(event) => setSearch(event.target.value)} placeholder="Search run or agent" value={search} />
          </label>
        </div>

        {query.isPending ? (
          <div className="loading-state"><LoaderCircle className="spin" size={18} /> Loading runs</div>
        ) : query.isError ? (
          <div className="error-state">Runway is unavailable. The dashboard will retry automatically.</div>
        ) : filteredRuns.length === 0 ? (
          <div className="empty-state">No matching runs.</div>
        ) : (
          <div className="run-table-wrap">
            <table className="run-table">
              <thead><tr><th>Run</th><th>Status</th><th>Executions</th><th>Events</th><th>Last activity</th></tr></thead>
              <tbody>
                {filteredRuns.map((run) => (
                  <tr key={run.run_id}>
                    <td>
                      <Link className="run-link" to={`/runs/${run.run_id}`}>
                        <strong>{run.agent_id}</strong>
                        <code>{run.run_id}</code>
                      </Link>
                    </td>
                    <td><StatusBadge status={run.execution_status} /></td>
                    <td>{run.execution_count}</td>
                    <td>{run.event_count}</td>
                    <td title={formatTimestamp(run.last_activity_at)}>{formatRelative(run.last_activity_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
