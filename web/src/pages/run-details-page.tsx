import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft } from "lucide-react"
import { Link, useParams } from "react-router-dom"
import { getRun, loadEvents, errorMessage } from "@/lib/runtime"
import type { RuntimeEvent } from "@/lib/runtime"
import { RunTrace } from "@/components/run-trace"
import { traceTone } from "@/lib/run-trace"

export function RunDetailsPage() {
  const { runID = "" } = useParams()
  const cache = useQueryClient()
  const detail = useQuery({
    queryKey: ["run", runID],
    queryFn: ({ signal }) => getRun(runID, signal),
    refetchInterval: 2500,
  })
  const events = useQuery({
    queryKey: ["run-events", runID],
    queryFn: ({ signal }) =>
      loadEvents(
        runID,
        cache.getQueryData<RuntimeEvent[]>(["run-events", runID]) ?? [],
        signal,
      ),
    refetchInterval: 2500,
  })
  return (
    <div className="page run-trace-page">
      <Link className="knowledge-review-back" to="/admin/runs">
        <ArrowLeft size={15} /> Runs
      </Link>
      <header className="page-header">
        <div>
          <div className="eyebrow">ADMIN · RUNTIME TRACE</div>
          <h1>{detail.data?.run.name || "Run"}</h1>
          <code>{runID}</code>
        </div>
        <Link className="primary-button" to={`/chat/${runID}`}>
          Open chat
        </Link>
      </header>
      {(detail.error || events.error) && (
        <p role="alert" className="runtime-error">
          {errorMessage(detail.error || events.error)}
        </p>
      )}
      {detail.isPending && <p className="loading-state">Loading…</p>}
      <div className="trace-run-summary">
        {detail.data && (
          <span
            className={`trace-status trace-tone-${traceTone(detail.data.run.status)}`}
          >
            {detail.data.run.status}
          </span>
        )}
        <span>{detail.data?.turns.length ?? 0} user turns</span>
        <span>{detail.data?.tasks.length ?? 0} delegated tasks</span>
        <span>{events.data?.length.toLocaleString() ?? 0} events</span>
        <span>Auto-refresh · 2.5s</span>
      </div>
      {events.isPending && <p className="loading-state">Loading trace…</p>}
      <RunTrace key={runID} detail={detail.data} events={events.data ?? []} />
    </div>
  )
}
