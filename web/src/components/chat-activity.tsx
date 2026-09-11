import {
  Bot,
  BrainCircuit,
  Check,
  ChevronDown,
  CircleAlert,
  Clock3,
  MessageSquareText,
  Wrench,
} from "lucide-react"
import type { ComponentType } from "react"

import { readNumber, readString } from "@/lib/format"
import type { ExecutionEvent, ExecutionSummary } from "@/lib/types"

interface ActivityItem {
  eventID: string
  label: string
  detail: string
  status?: string
  icon: ComponentType<{ size?: number }>
  danger?: boolean
}

function failureMessage(event: ExecutionEvent): string {
  const error = event.payload.error
  if (error !== null && typeof error === "object" && !Array.isArray(error)) {
    return readString(error as Record<string, unknown>, "message") ?? "Execution failed"
  }
  return "Execution failed"
}

function activityItems(events: ExecutionEvent[], executions: ExecutionSummary[]): ActivityItem[] {
  const resultsByCallID = new Map(
    events
      .filter((event) => event.type === "tool.result_v1" || event.type === "agent.result_v1")
      .map((event) => [event.entity_id, event]),
  )
  const agentsByExecutionID = new Map(executions.map((execution) => [execution.execution_id, execution.agent_id]))

  return events.flatMap((event): ActivityItem[] => {
    if (event.type === "model.response_v1") {
      const sequence = readNumber(event.payload, "sequence") ?? 1
      return [{
        eventID: event.event_id,
        label: `Model call ${sequence}`,
        detail: readString(event.payload, "model") ?? "Language model",
        icon: BrainCircuit,
      }]
    }

    if (event.type === "tool.call_v1") {
      const result = resultsByCallID.get(event.entity_id)
      return [{
        eventID: event.event_id,
        label: readString(event.payload, "tool_id") ?? "Tool call",
        detail: "Tool",
        status: result ? readString(result.payload, "status") ?? "completed" : "running",
        icon: Wrench,
      }]
    }

    if (event.type === "agent.call_v1") {
      const result = resultsByCallID.get(event.entity_id)
      return [{
        eventID: event.event_id,
        label: readString(event.payload, "agent_id") ?? "Delegated agent",
        detail: "Agent",
        status: result ? readString(result.payload, "status") ?? "completed" : "queued",
        icon: Bot,
      }]
    }

    if (event.type === "message.assistant_v1" && agentsByExecutionID.has(event.execution_id)) {
      const content = readString(event.payload, "content")
      return [{
        eventID: event.event_id,
        label: `${agentsByExecutionID.get(event.execution_id)} response`,
        detail: content || "Agent response checkpointed",
        status: "completed",
        icon: MessageSquareText,
      }]
    }

    if (event.type === "execution.lease_expired_v1") {
      return [{
        eventID: event.event_id,
        label: "Execution requeued",
        detail: agentsByExecutionID.get(event.execution_id) ?? event.execution_id,
        status: "lease expired",
        icon: Clock3,
      }]
    }

    if (event.type === "execution.failed_v1") {
      return [{
        eventID: event.event_id,
        label: "Execution failed",
        detail: failureMessage(event),
        status: "failed",
        icon: CircleAlert,
        danger: true,
      }]
    }

    return []
  })
}

interface ChatActivityProps {
  events: ExecutionEvent[]
  executions: ExecutionSummary[]
  active: boolean
  startedAt?: string
  completedAt?: string
}

function formatDuration(startedAt?: string, completedAt?: string): string | null {
  if (!startedAt || !completedAt) return null
  const elapsedMilliseconds = Date.parse(completedAt) - Date.parse(startedAt)
  if (!Number.isFinite(elapsedMilliseconds) || elapsedMilliseconds < 0) return null
  const elapsedSeconds = elapsedMilliseconds / 1_000
  const roundedSeconds = elapsedSeconds < 10
    ? Math.round(elapsedSeconds * 10) / 10
    : Math.round(elapsedSeconds)
  return `${roundedSeconds} ${roundedSeconds === 1 ? "second" : "seconds"}`
}

export function ChatActivity({ events, executions, active, startedAt, completedAt }: ChatActivityProps) {
  const items = activityItems(events, executions)
  const failed = items.some((item) => item.danger)
  const duration = formatDuration(startedAt, completedAt)
  if (!active && items.length === 0) return null

  return (
    <details
      className={`chat-activity${active ? " active" : ""}${failed ? " failed" : ""}`}
      open={active || undefined}
    >
      <summary>
        <span className="chat-activity-state">
          {active ? <span className="thinking-orbit"><i /><i /><i /></span> : failed ? <CircleAlert size={14} /> : <Check size={14} />}
          {active ? "Blix is working" : failed ? "Run failed" : `${items.length} ${items.length === 1 ? "step" : "steps"} completed`}
        </span>
        <span className="chat-activity-summary-meta">
          {!active && duration && <span>Finished in {duration}</span>}
          {items.length > 0 && <ChevronDown className="chat-activity-chevron" size={14} />}
        </span>
      </summary>
      {items.length > 0 && (
        <div className="chat-activity-list">
          {items.map(({ eventID, label, detail, status, icon: Icon, danger }) => (
            <div className={`chat-activity-item${danger ? " danger" : ""}`} key={eventID}>
              <span className="chat-activity-icon"><Icon size={13} /></span>
              <span className="chat-activity-copy"><strong>{label}</strong><small>{detail}</small></span>
              {status && <span className="chat-activity-status">{status}</span>}
            </div>
          ))}
        </div>
      )}
    </details>
  )
}
