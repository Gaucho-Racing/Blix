import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, Clock3, Database, GitFork, LoaderCircle, Radio } from "lucide-react"
import { useState } from "react"
import { Link, useParams } from "react-router-dom"

import { EventTimeline } from "@/components/event-timeline"
import { RunEventStream } from "@/components/event-stream"
import { ExecutionInspector } from "@/components/execution-inspector"
import { ExecutionList } from "@/components/execution-list"
import { StatusBadge } from "@/components/status-badge"
import { getRun, getRunEvents } from "@/lib/api"
import { formatIdentifier, formatTimestamp } from "@/lib/format"

export function RunDetailsPage() {
  const { runID = "" } = useParams()
  const [selectedExecutionID, setSelectedExecutionID] = useState("")
  const runQuery = useQuery({
    queryKey: ["run", runID],
    queryFn: () => getRun(runID),
    enabled: Boolean(runID),
  })
  const eventsQuery = useQuery({
    queryKey: ["run-events", runID],
    queryFn: () => getRunEvents(runID),
    enabled: Boolean(runID),
  })

  if (runQuery.isPending) {
    return <div className="page centered"><div className="loading-state"><LoaderCircle className="spin" size={18} /> Loading run</div></div>
  }
  if (runQuery.isError) {
    return <div className="page centered"><div className="error-state">This run could not be loaded.</div></div>
  }

  const { run, executions } = runQuery.data
  const effectiveExecutionID = executions.some((execution) => execution.execution_id === selectedExecutionID)
    ? selectedExecutionID
    : run.root_execution_id
  const selected = executions.find((execution) => execution.execution_id === effectiveExecutionID) ?? executions[0]
  const selectedEvents = (eventsQuery.data ?? []).filter((event) => event.execution_id === effectiveExecutionID)

  return (
    <div className="page run-detail-page">
      <Link className="back-link" to="/admin/runs"><ArrowLeft size={15} /> All runs</Link>
      <header className="run-header">
        <div>
          <div className="run-title-line"><h1>{run.agent_id}</h1><StatusBadge status={run.execution_status} /></div>
          <code>{formatIdentifier(run.run_id, "run")}</code>
        </div>
        <div className="run-header-meta">
          <span><GitFork size={14} /> {run.execution_count} executions</span>
          <span><Database size={14} /> {run.event_count} events</span>
          <span><Clock3 size={14} /> {formatTimestamp(run.created_at)}</span>
          {eventsQuery.data && (
            <RunEventStream
              initialCursor={eventsQuery.data.at(-1)?.position ?? run.last_event_position}
              key={runID}
              runID={runID}
            />
          )}
        </div>
      </header>

      <div className="detail-grid">
        <section className="panel execution-panel">
          <div className="section-heading">
            <div><span className="section-kicker">RUN GRAPH</span><h2>Executions</h2></div>
            <span className="count-pill">{executions.length}</span>
          </div>
          <ExecutionList executions={executions} onSelect={setSelectedExecutionID} selectedID={selected?.execution_id ?? ""} />
        </section>

        <section className="panel inspector-panel">
          <div className="section-heading">
            <div><span className="section-kicker">CHECKPOINT</span><h2>{selected?.agent_id ?? "Execution"}</h2></div>
            {selected && <StatusBadge status={selected.status} />}
          </div>
          {selected && <ExecutionInspector executionID={selected.execution_id} />}
        </section>

        <section className="panel timeline-panel">
          <div className="section-heading">
            <div><span className="section-kicker">EVENT STREAM</span><h2>Timeline</h2></div>
            <span className="stream-state"><Radio size={13} /> {selectedEvents.length} persisted</span>
          </div>
          {eventsQuery.isError ? (
            <div className="error-state compact">Events could not be loaded.</div>
          ) : (
            <EventTimeline events={selectedEvents} />
          )}
        </section>
      </div>
    </div>
  )
}
