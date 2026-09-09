import { useQuery } from "@tanstack/react-query"
import { KeyRound, LoaderCircle } from "lucide-react"
import { useState } from "react"

import { getExecution } from "@/lib/api"
import { formatIdentifier, formatIdentifiersInJSON, formatTimestamp } from "@/lib/format"

interface ExecutionInspectorProps {
  executionID: string
}

type InspectorTab = "state" | "task" | "result"

export function ExecutionInspector({ executionID }: ExecutionInspectorProps) {
  const [tab, setTab] = useState<InspectorTab>("state")
  const query = useQuery({
    queryKey: ["execution", executionID],
    queryFn: () => getExecution(executionID),
    refetchInterval: 3_000,
  })

  if (query.isPending) {
    return <div className="loading-state compact"><LoaderCircle className="spin" size={16} /> Loading execution</div>
  }
  if (query.isError) {
    return <div className="error-state compact">Could not load this execution.</div>
  }

  const details = query.data
  const inspected = details[tab]
  return (
    <div className="inspector">
      <dl className="metadata-grid">
        <div><dt>Version</dt><dd>{details.execution.version}</dd></div>
        <div><dt>Updated</dt><dd>{formatTimestamp(details.execution.updated_at)}</dd></div>
        <div><dt>Worker</dt><dd className="mono">{details.execution.claimed_by ? formatIdentifier(details.execution.claimed_by, "worker") : "—"}</dd></div>
        <div><dt>Lease expires</dt><dd>{details.execution.lease_expires_at ? formatTimestamp(details.execution.lease_expires_at) : "—"}</dd></div>
      </dl>

      {details.has_provider_continuation && (
        <div className="continuation-note">
          <KeyRound size={14} /> Provider continuation retained server-side
        </div>
      )}

      <div className="tab-list" role="tablist">
        {(["state", "task", "result"] as const).map((value) => (
          <button
            aria-selected={tab === value}
            className={tab === value ? "active" : ""}
            key={value}
            onClick={() => setTab(value)}
            role="tab"
            type="button"
          >
            {value}
          </button>
        ))}
      </div>
      <pre className="json-block inspector-json">{inspected == null ? "No data" : JSON.stringify(formatIdentifiersInJSON(inspected), null, 2)}</pre>
    </div>
  )
}
