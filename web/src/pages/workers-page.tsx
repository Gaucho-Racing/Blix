import { useQuery } from "@tanstack/react-query"
import { Activity, Boxes, Cpu, LoaderCircle, Search, ServerCog } from "lucide-react"
import { useEffect, useMemo, useState } from "react"

import { WorkerListEventStream } from "@/components/event-stream"
import { StatusBadge } from "@/components/status-badge"
import { listWorkers } from "@/lib/api"
import { formatCompactNumber, formatDuration, formatRelative, formatTimestamp } from "@/lib/format"
import type { WorkerStatus, WorkerSummary } from "@/lib/types"

const noWorkers: WorkerSummary[] = []

function currentStatus(worker: WorkerSummary, now: number): WorkerStatus {
  if (now === 0) return worker.status
  if (new Date(worker.heartbeat_expires_at).getTime() <= now) return "offline"
  if (worker.active_executions === 0) return "idle"
  if (worker.active_executions >= worker.concurrency) return "saturated"
  return "active"
}

function workerLifetime(worker: WorkerSummary, status: WorkerStatus, now: number): number {
  const end = status === "offline" || now === 0 ? new Date(worker.last_seen_at).getTime() : now
  return end - new Date(worker.registered_at).getTime()
}

export function WorkersPage() {
  const [search, setSearch] = useState("")
  const [now, setNow] = useState(0)
  const query = useQuery({ queryKey: ["workers"], queryFn: listWorkers })
  const workers = query.data ?? noWorkers

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [])

  const visibleWorkers = useMemo(() => {
    const needle = search.trim().toLowerCase()
    if (!needle) return workers
    return workers.filter((worker) =>
      [worker.worker_id, currentStatus(worker, now)].some((value) => value.toLowerCase().includes(needle)),
    )
  }, [now, search, workers])

  const onlineWorkers = workers.filter((worker) => currentStatus(worker, now) !== "offline")
  const activeExecutions = onlineWorkers.reduce((total, worker) => total + worker.active_executions, 0)
  const totalCapacity = onlineWorkers.reduce((total, worker) => total + worker.concurrency, 0)
  const availableCapacity = Math.max(0, totalCapacity - activeExecutions)

  return (
    <div className="page workers-page">
      <header className="page-header">
        <div>
          <div className="eyebrow">ADMIN · RUNTIME</div>
          <h1>Agent workers</h1>
          <p>Live process incarnations, execution capacity, and lifetime processing statistics.</p>
        </div>
        <WorkerListEventStream />
      </header>

      <section className="stat-grid" aria-label="Worker summary">
        <article className="stat-card"><ServerCog size={17} /><div><span>Online workers</span><strong>{onlineWorkers.length}</strong></div></article>
        <article className="stat-card"><Activity size={17} /><div><span>Active executions</span><strong>{activeExecutions}</strong></div></article>
        <article className="stat-card"><Boxes size={17} /><div><span>Available slots</span><strong>{availableCapacity}</strong></div></article>
        <article className="stat-card"><Cpu size={17} /><div><span>Total capacity</span><strong>{totalCapacity}</strong></div></article>
      </section>

      <section className="panel workers-panel">
        <div className="panel-toolbar">
          <div>
            <h2>Worker lifetimes</h2>
            <span>{visibleWorkers.length} of {workers.length} process incarnations</span>
          </div>
          <label className="search-box">
            <Search size={15} />
            <input onChange={(event) => setSearch(event.target.value)} placeholder="Search worker or status" value={search} />
          </label>
        </div>

        {query.isPending ? (
          <div className="loading-state"><LoaderCircle className="spin" size={18} /> Loading workers</div>
        ) : query.isError ? (
          <div className="error-state">Runway is unavailable. The dashboard will retry automatically.</div>
        ) : visibleWorkers.length === 0 ? (
          <div className="empty-state">No workers have registered yet.</div>
        ) : (
          <div className="worker-table-wrap">
            <table className="worker-table">
              <colgroup>
                <col className="worker-column-identity" />
                <col className="worker-column-status" />
                <col className="worker-column-lifetime" />
                <col className="worker-column-workload" />
                <col className="worker-column-outcomes" />
                <col className="worker-column-calls" />
                <col className="worker-column-tokens" />
                <col className="worker-column-heartbeat" />
              </colgroup>
              <thead>
                <tr><th>Worker</th><th>Status</th><th>Lifetime</th><th>Workload</th><th>Outcomes</th><th>Calls</th><th>Tokens</th><th>Heartbeat</th></tr>
              </thead>
              <tbody>
                {visibleWorkers.map((worker) => {
                  const status = currentStatus(worker, now)
                  const completed = worker.executions_succeeded + worker.executions_failed + worker.executions_cancelled
                  return (
                    <tr key={worker.worker_id}>
                      <td><div className="worker-identity"><strong>Agent worker</strong><code>{worker.worker_id}</code></div></td>
                      <td><StatusBadge status={status} /></td>
                      <td title={`Registered ${formatTimestamp(worker.registered_at)}`}>
                        <strong className="table-primary">{formatDuration(workerLifetime(worker, status, now))}</strong>
                        <span className="table-secondary">{status === "offline" ? "final observed" : "current uptime"}</span>
                      </td>
                      <td>
                        <strong className="table-primary">{worker.active_executions} / {worker.concurrency}</strong>
                        <span className="table-secondary">active / capacity</span>
                      </td>
                      <td>
                        <strong className="table-primary">{completed} completed</strong>
                        <span className="table-secondary">{worker.executions_succeeded} ok · {worker.executions_failed} failed · {worker.executions_claimed} claims</span>
                      </td>
                      <td>
                        <strong className="table-primary">{worker.model_calls} model</strong>
                        <span className="table-secondary">{worker.tool_calls} tool</span>
                      </td>
                      <td>
                        <strong className="table-primary">{formatCompactNumber(worker.input_tokens + worker.output_tokens)}</strong>
                        <span className="table-secondary">{formatCompactNumber(worker.input_tokens)} in · {formatCompactNumber(worker.output_tokens)} out</span>
                      </td>
                      <td title={formatTimestamp(worker.last_seen_at)}>
                        <strong className="table-primary">{formatRelative(worker.last_seen_at)}</strong>
                        <span className="table-secondary">last seen</span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
