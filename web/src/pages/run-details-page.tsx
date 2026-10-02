import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft } from "lucide-react"
import { Link, useParams } from "react-router-dom"
import { getRun, loadEvents, errorMessage } from "@/lib/runtime"
import type { RuntimeEvent } from "@/lib/runtime"
import { formatTimestamp } from "@/lib/format"

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
        cache.getQueryData<RuntimeEvent[]>(["run-events", runID]) ??
          [],
        signal,
      ),
    refetchInterval: 2500,
  })
  return (
    <div className="page">
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
      <section className="panel wiki-reader">
        <h2>Turns</h2>
        {detail.data?.turns.map((turn) => (
          <p key={turn.turn_id}>
            <code>{turn.turn_id}</code> · {turn.status}
          </p>
        ))}
        <h2>Tasks</h2>
        {detail.data?.tasks.map((task) => (
          <p key={task.task_id}>
            {task.objective} · {task.status}
          </p>
        ))}
      </section>
      <section className="panel wiki-reader">
        <h2>Chronological activity</h2>
        <div className="runtime-trace">
          {events.data?.map((event) => (
            <details key={event.event_id}>
              <summary>
                <span>#{event.position}</span> {event.type}{" "}
                <small>{event.task_id ? "task" : "lead"}</small>
              </summary>
              <pre>
                {JSON.stringify(
                  {
                    turn_id: event.turn_id,
                    task_id: event.task_id,
                    session_id: event.session_id,
                    payload: event.payload,
                  },
                  null,
                  2,
                )}
              </pre>
            </details>
          ))}
        </div>
        <small>
          Updated{" "}
          {detail.data && formatTimestamp(detail.data.run.updated_at)}
        </small>
      </section>
    </div>
  )
}
