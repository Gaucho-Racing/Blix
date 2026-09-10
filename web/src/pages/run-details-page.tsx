import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  Clock3,
  Database,
  GitFork,
  ListTree,
  LoaderCircle,
  Network,
  Radio,
  ShieldAlert,
  ShieldCheck,
  ShieldEllipsis,
} from "lucide-react"
import { lazy, Suspense, useState } from "react"
import { Link, useParams } from "react-router-dom"

import { EventTimeline } from "@/components/event-timeline"
import { RunEventStream } from "@/components/event-stream"
import { ExecutionInspector } from "@/components/execution-inspector"
import { ExecutionList } from "@/components/execution-list"
import { IdentityDisplay } from "@/components/identity-display"
import { StatusBadge } from "@/components/status-badge"
import { getRun, getRunEvents } from "@/lib/api"
import { formatTimestamp, runDisplayStatus } from "@/lib/format"
import { useIdentities } from "@/lib/identity"
import type { CallerVerifiedPayload, ExecutionEvent } from "@/lib/types"
import { useNow } from "@/lib/use-now"

const RunDag = lazy(() => import("@/components/run-dag").then((module) => ({ default: module.RunDag })))

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function callerVerificationFailure(events: ExecutionEvent[]) {
  return [...events].reverse().find((event) => {
    if (event.type !== "execution.failed_v1" || !isRecord(event.payload.error)) return false
    const code = event.payload.error.code
    return typeof code === "string" && (code.startsWith("caller_") || code.startsWith("sentinel_"))
  })
}

export function RunDetailsPage() {
  const { runID = "" } = useParams()
  const [selectedExecutionID, setSelectedExecutionID] = useState("")
  const [viewMode, setViewMode] = useState<"timeline" | "graph">("timeline")
  const now = useNow()
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
  const callerEntityID = runQuery.data?.run.caller_entity_id ?? ""
  const identityQuery = useIdentities([callerEntityID])

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
  const events = eventsQuery.data ?? []
  const selectedEvents = events.filter((event) => event.execution_id === effectiveExecutionID)
  const callerIdentity = identityQuery.identities.find((identity) => identity.id === run.caller_entity_id)
  const verificationEvent = [...events].reverse().find((event) => event.type === "caller.verified_v1")
  const verification = verificationEvent?.payload as unknown as CallerVerifiedPayload | undefined
  const verificationFailure = callerVerificationFailure(events)
  const failure = isRecord(verificationFailure?.payload.error) ? verificationFailure.payload.error : undefined
  const identityID = run.caller_context?.user_id || run.caller_context?.service_account_id

  return (
    <div className="page run-detail-page">
      <Link className="back-link" to="/admin/runs"><ArrowLeft size={15} /> All runs</Link>
      <header className="run-header">
        <div>
          <div className="run-title-line"><h1>{run.agent_id}</h1><StatusBadge status={runDisplayStatus(run, now)} /></div>
          <code>{run.run_id}</code>
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

      <section className="panel caller-panel">
        <div className="section-heading">
          <div><span className="section-kicker">CALLER CONTEXT</span><h2>Authenticated identity</h2></div>
          {verificationEvent ? (
            <span className="verification-badge verified"><ShieldCheck size={13} /> Verified</span>
          ) : verificationFailure ? (
            <span className="verification-badge denied"><ShieldAlert size={13} /> Denied</span>
          ) : (
            <span className="verification-badge pending"><ShieldEllipsis size={13} /> Not checkpointed</span>
          )}
        </div>
        <div className="caller-panel-body">
          <div className="caller-current-identity">
            <span className="caller-label">Current Sentinel identity</span>
            <IdentityDisplay entityID={run.caller_entity_id} identity={callerIdentity} />
          </div>
          <dl className="caller-context-grid">
            <div><dt>Entity type</dt><dd>{run.caller_context?.entity_type || callerIdentity?.type || "Unknown"}</dd></div>
            <div><dt>Identity ID</dt><dd><code>{identityID || "—"}</code></dd></div>
            <div><dt>Entity ID</dt><dd><code>{run.caller_entity_id || "—"}</code></dd></div>
            <div><dt>Verified by</dt><dd><code>{verification?.worker_id || "—"}</code></dd></div>
          </dl>
          {failure && (
            <div className="caller-verification-error">
              <strong>{typeof failure.code === "string" ? failure.code : "verification_failed"}</strong>
              <span>{typeof failure.message === "string" ? failure.message : "Caller verification failed."}</span>
            </div>
          )}
        </div>
      </section>

      {viewMode === "timeline" ? (
        <div className="detail-grid">
          <section className="panel execution-panel">
            <div className="section-heading">
              <div><span className="section-kicker">RUN GRAPH</span><h2>Executions</h2></div>
              <div className="section-actions">
                <span className="count-pill">{executions.length}</span>
                <button className="view-switch-button" onClick={() => setViewMode("graph")} type="button">
                  <Network size={13} /> Graph
                </button>
              </div>
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
              <div className="section-actions">
                <span className="stream-state"><Radio size={13} /> {selectedEvents.length} persisted</span>
                <button className="view-switch-button" onClick={() => setViewMode("graph")} type="button">
                  <Network size={13} /> Graph
                </button>
              </div>
            </div>
            {eventsQuery.isError ? (
              <div className="error-state compact">Events could not be loaded.</div>
            ) : (
              <EventTimeline events={selectedEvents} />
            )}
          </section>
        </div>
      ) : (
        <section className="panel run-dag-panel">
          <div className="section-heading">
            <div><span className="section-kicker">RUN DAG</span><h2>Full execution graph</h2></div>
            <div className="section-actions">
              <span className="stream-state">{executions.length} executions · {events.length} events</span>
              <button className="view-switch-button active" onClick={() => setViewMode("timeline")} type="button">
                <ListTree size={13} /> Timeline
              </button>
            </div>
          </div>
          {eventsQuery.isError ? (
            <div className="error-state">Events could not be loaded.</div>
          ) : (
            <Suspense fallback={<div className="loading-state"><LoaderCircle className="spin" size={18} /> Loading graph</div>}>
              <RunDag
                events={events}
                executions={executions}
                onSelectExecution={setSelectedExecutionID}
                selectedExecutionID={selected?.execution_id ?? ""}
              />
            </Suspense>
          )}
        </section>
      )}
    </div>
  )
}
