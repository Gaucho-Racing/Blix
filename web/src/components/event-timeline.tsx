import {
  Bot,
  BrainCircuit,
  CheckCircle2,
  CircleAlert,
  Clock3,
  CornerDownRight,
  MessageSquareText,
  MousePointerClick,
  Pause,
  Play,
  UserRound,
  Wrench,
} from "lucide-react"
import type { ComponentType } from "react"

import {
  eventLabel,
  formatIdentifier,
  formatIdentifiersInJSON,
  formatTimestamp,
  readNumber,
  readString,
  type IdentifierPrefix,
} from "@/lib/format"
import type { ExecutionEvent } from "@/lib/types"

const icons: Record<string, ComponentType<{ size?: number }>> = {
  "message.user_v1": UserRound,
  "message.assistant_v1": MessageSquareText,
  "model.response_v1": BrainCircuit,
  "tool.call_v1": Wrench,
  "tool.result_v1": CheckCircle2,
  "agent.call_v1": CornerDownRight,
  "agent.result_v1": Bot,
  "execution.claimed_v1": MousePointerClick,
  "execution.suspended_v1": Pause,
  "execution.resumed_v1": Play,
  "execution.lease_expired_v1": Clock3,
  "execution.failed_v1": CircleAlert,
}

function entityPrefix(eventType: string): IdentifierPrefix {
  if (eventType.startsWith("message.")) return "message"
  if (eventType.startsWith("tool.") || eventType.startsWith("agent.")) return "call"
  if (eventType.startsWith("model.")) return "model_call"
  return "execution"
}

function eventSummary(event: ExecutionEvent): string {
  const content = readString(event.payload, "content")
  if (content) return content

  if (event.type === "model.response_v1") {
    const model = readString(event.payload, "model") ?? "model"
    const input = readNumber(event.payload, "input_tokens") ?? 0
    const output = readNumber(event.payload, "output_tokens") ?? 0
    return `${model} · ${input.toLocaleString()} in / ${output.toLocaleString()} out`
  }

  const identity = ["tool_id", "agent_id", "worker_id", "status"]
    .map((key) => readString(event.payload, key))
    .find(Boolean)
  return identity ?? formatIdentifier(event.entity_id, entityPrefix(event.type))
}

interface EventTimelineProps {
  events: ExecutionEvent[]
}

export function EventTimeline({ events }: EventTimelineProps) {
  if (events.length === 0) {
    return <div className="empty-state compact">No events have been checkpointed yet.</div>
  }

  return (
    <div className="timeline">
      {events.map((event) => {
        const Icon = icons[event.type] ?? Clock3
        return (
          <details className={`timeline-event event-${event.type.split(".")[0]}`} key={event.event_id}>
            <summary>
              <span className="timeline-marker"><Icon size={14} /></span>
              <span className="timeline-copy">
                <span className="timeline-heading">
                  <strong>{eventLabel(event.type)}</strong>
                  <time dateTime={event.created_at}>{formatTimestamp(event.created_at)}</time>
                </span>
                <span className="timeline-summary">{eventSummary(event)}</span>
                <span className="timeline-meta">
                  <code>#{event.position}</code>
                  <code>{formatIdentifier(event.event_id, "event")}</code>
                  <code>{formatIdentifier(event.execution_id, "execution")}</code>
                </span>
              </span>
            </summary>
            <pre className="json-block">{JSON.stringify(formatIdentifiersInJSON(event.payload), null, 2)}</pre>
          </details>
        )
      })}
    </div>
  )
}
