import { Bot, CornerDownRight } from "lucide-react"

import { StatusBadge } from "@/components/status-badge"
import type { ExecutionSummary } from "@/lib/types"

interface ExecutionListProps {
  executions: ExecutionSummary[]
  selectedID: string
  onSelect: (executionID: string) => void
}

function executionDepth(execution: ExecutionSummary, byID: Map<string, ExecutionSummary>): number {
  let depth = 0
  let parentID = execution.parent_execution_id
  const visited = new Set<string>()
  while (parentID && !visited.has(parentID)) {
    visited.add(parentID)
    depth += 1
    parentID = byID.get(parentID)?.parent_execution_id ?? null
  }
  return depth
}

export function ExecutionList({ executions, selectedID, onSelect }: ExecutionListProps) {
  const byID = new Map(executions.map((execution) => [execution.execution_id, execution]))

  return (
    <div className="execution-list">
      {executions.map((execution) => {
        const depth = executionDepth(execution, byID)
        return (
          <button
            className={`execution-item${selectedID === execution.execution_id ? " selected" : ""}`}
            key={execution.execution_id}
            onClick={() => onSelect(execution.execution_id)}
            style={{ paddingLeft: `${16 + depth * 22}px` }}
            type="button"
          >
            <span className="execution-icon">
              {depth > 0 ? <CornerDownRight size={15} /> : <Bot size={15} />}
            </span>
            <span className="execution-copy">
              <strong>{execution.agent_id}</strong>
              <code>{execution.execution_id}</code>
            </span>
            <StatusBadge status={execution.status} />
          </button>
        )
      })}
    </div>
  )
}
