import { eventLabel, readString } from "@/lib/format"
import type { RunDetails, RuntimeEvent } from "@/lib/runtime"

export type TraceTone = "neutral" | "active" | "waiting" | "success" | "error"
export interface TraceActor {
  id: string
  name: string
  instance?: number
  kind: "user" | "lead" | "agent" | "tool" | "system"
}
export interface TraceEntry {
  event: RuntimeEvent
  from: TraceActor
  to?: TraceActor
  title: string
  content: string
  tone: TraceTone
  status: string
  category: "conversation" | "handoff" | "tool" | "system"
  taskIDs: string[]
  routine: boolean
}
export function traceTone(status: string): TraceTone {
  if (/failed|error|expired|rejected/.test(status)) return "error"
  if (/waiting|suspended|approval/.test(status)) return "waiting"
  if (/succeeded|completed|delivered|approved|success/.test(status))
    return "success"
  if (/running|queued|started|pending/.test(status)) return "active"
  return "neutral"
}
export const leadActor: TraceActor = {
  id: "lead",
  name: "Blix lead",
  kind: "lead",
}
const user: TraceActor = { id: "user", name: "User", kind: "user" }
const runtime: TraceActor = { id: "system", name: "Runtime", kind: "system" }
function object(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}
function agentName(id?: string) {
  if (id === "blix_code_v1") return "blix_code_v1"
  return id ? id.replace(/_v\d+$/, "").replaceAll("_", " ") : "Agent"
}
function readable(value: unknown): string {
  if (typeof value === "string") return value
  if (value === undefined || value === null) return ""
  return JSON.stringify(value, null, 2)
}
export function buildTrace(
  events: RuntimeEvent[],
  detail?: RunDetails,
): TraceEntry[] {
  const actors = new Map<string, TraceActor>()
  const sessions = new Map<string, TraceActor>()
  const parents = new Map<string, string>()
  const tools = new Map<string, string>()
  for (const event of events) {
    if (event.type === "tool.call_v1")
      tools.set(event.entity_id, readString(event.payload, "tool_id") ?? "Tool")
    if (event.type === "task.created_v1") {
      const id = readString(event.payload, "task_id")
      if (!id) continue
      const actor: TraceActor = {
        id,
        name: agentName(readString(event.payload, "agent_id")),
        instance: actors.size + 1,
        kind: "agent",
      }
      actors.set(id, actor)
      sessions.set(
        readString(event.payload, "session_id") ?? event.entity_id,
        actor,
      )
      parents.set(id, event.session_id)
    }
  }
  for (const task of detail?.tasks ?? []) {
    if (!actors.has(task.task_id))
      actors.set(task.task_id, {
        id: task.task_id,
        name: "Agent",
        kind: "agent",
      })
    if (task.session_id)
      sessions.set(task.session_id, actors.get(task.task_id)!)
    if (task.parent_session_id)
      parents.set(task.task_id, task.parent_session_id)
  }
  const sessionActor = (id: string) =>
    sessions.get(id) ??
    (id === detail?.run.lead_session_id ? leadActor : runtime)
  return [...events]
    .sort((a, b) => a.position - b.position)
    .map((event) => {
      const p = event.payload
      const taskID = readString(p, "task_id") ?? event.task_id
      const taskActor = taskID ? actors.get(taskID) : undefined
      const actor = event.task_id
        ? (actors.get(event.task_id) ?? sessionActor(event.session_id))
        : sessionActor(event.session_id)
      const parent = taskID
        ? sessionActor(parents.get(taskID) ?? detail?.run.lead_session_id ?? "")
        : leadActor
      const result = object(p.result)
      const output = object(result.output)
      const error = object(p.error ?? result.error)
      const status =
        readString(p, "status") ?? readString(result, "status") ?? ""
      let entry: TraceEntry = {
        event,
        from: actor,
        title: eventLabel(event.type),
        content: readString(p, "content") ?? "",
        status,
        tone: traceTone(status),
        category: "system",
        taskIDs: [
          ...new Set(
            [
              event.task_id,
              taskID,
              actor.kind === "agent" ? actor.id : undefined,
            ].filter((id): id is string => !!id),
          ),
        ],
        routine:
          /^(model\.|caller\.|attempt\.claimed|run\.created|artifact\.)/.test(
            event.type,
          ),
      }
      switch (event.type) {
        case "message.user_v1":
          entry = {
            ...entry,
            from: user,
            to: leadActor,
            category: "conversation",
            title: "User message",
            content: readString(p, "display_content") || entry.content,
          }
          break
        case "message.assistant_v1":
          entry = {
            ...entry,
            to: actor.kind === "lead" ? user : parent,
            category: "conversation",
            title: "Message",
            content: entry.content,
          }
          break
        case "task.created_v1":
          entry = {
            ...entry,
            from: sessionActor(event.session_id),
            to: taskActor,
            category: "handoff",
            title: "Delegated task",
            content: readString(p, "objective") ?? "",
            status: p.background ? "Background task" : "Delegated",
            tone: "active",
            taskIDs: [
              ...new Set([
                ...entry.taskIDs,
                ...[sessionActor(event.session_id)]
                  .filter((a) => a.kind === "agent")
                  .map((a) => a.id),
              ]),
            ],
          }
          break
        case "task.result_v1":
          entry = {
            ...entry,
            from: taskActor ?? actor,
            to: parent,
            category: "handoff",
            title: "Task returned",
            content:
              readString(error, "message") ??
              readString(output, "summary") ??
              readString(output, "message") ??
              readable(output),
          }
          break
        case "runtime.question_v1":
          entry = {
            ...entry,
            to: parent,
            category: "handoff",
            title: "Clarification requested",
            tone: "waiting",
            status: "Waiting for an answer",
            content: Array.isArray(p.questions)
              ? p.questions
                  .map((q) => {
                    const item = object(q)
                    return `${readString(item, "question") ?? ""}${Array.isArray(item.options) ? "\nOptions: " + item.options.map((o) => readString(object(o), "label")).join(" · ") : ""}`
                  })
                  .join("\n\n")
              : entry.content,
          }
          break
        case "task.answered_v1":
          entry = {
            ...entry,
            from: parent,
            to: taskActor ?? actor,
            category: "handoff",
            title: "Answer queued",
            status: "Delivery pending",
            tone: "active",
            content: Array.isArray(p.answers)
              ? p.answers
                  .map((a) => {
                    const answer = object(a)
                    return (
                      readString(answer, "text") ||
                      (Array.isArray(answer.selected_options)
                        ? answer.selected_options.join(", ")
                        : "")
                    )
                  })
                  .join("\n")
              : readable(p),
          }
          break
        case "task.steered_v1":
          entry = {
            ...entry,
            from: parent,
            to: taskActor ?? actor,
            category: "handoff",
            title: "Task instructions updated",
          }
          break
        case "tool.call_v1":
        case "tool.result_v1": {
          const tool: TraceActor = {
            id: event.entity_id,
            name:
              tools.get(event.entity_id) ?? readString(p, "tool_id") ?? "Tool",
            kind: "tool",
          }
          const call = event.type === "tool.call_v1"
          entry = {
            ...entry,
            from: call ? actor : tool,
            to: call ? tool : actor,
            category: "tool",
            title: call ? "Tool call" : "Tool result",
            content: readable(call ? p.arguments : (p.output ?? p.error ?? p)),
            status: status || (call ? "Started" : "Returned"),
            tone: call ? "active" : traceTone(status),
          }
          break
        }
        case "runtime.coding_trace_v1": {
          const kind = readString(p, "kind") ?? "activity"
          const toolName = readString(p, "tool_name")
          const tool: TraceActor = {
            id: "pi-tool",
            name: toolName ?? "Sandbox",
            kind: "tool",
          }
          const call = kind === "tool_execution_start"
          const done = kind === "tool_execution_end"
          entry = {
            ...entry,
            title: kind.replaceAll("_", " "),
            category:
              call || done || kind === "tool_execution_update"
                ? "tool"
                : "system",
            from: done ? tool : actor,
            to: call ? tool : done ? actor : undefined,
            status: p.is_error
              ? "Error"
              : done
                ? "Completed"
                : kind === "answer_delivered"
                  ? "Delivered to blix_code_v1"
                  : kind === "question"
                    ? "Waiting"
                    : call
                      ? "Started"
                      : "",
            tone: p.is_error
              ? "error"
              : done || kind === "answer_delivered"
                ? "success"
                : kind === "question"
                  ? "waiting"
                  : call ? "active" : "neutral",
            routine:
              [
                "tool_execution_update",
                "agent_start",
                "agent_end",
                "turn_start",
                "session_settled",
                "prompt_result",
                "question",
              ].includes(kind) && p.is_error !== true,
          }
          break
        }
        case "runtime.progress_v1":
          entry = {
            ...entry,
            title: "Agent status",
            content: readString(p, "repository") ?? entry.content,
          }
          break
        case "approval.requested_v1":
          entry = {
            ...entry,
            to: user,
            title: "Approval requested",
            status: "Needs approval",
            tone: "waiting",
          }
          break
        case "attempt.failed_v1":
        case "model.failure_v1":
          entry = {
            ...entry,
            title: event.type.startsWith("model")
              ? "Model request failed"
              : "Execution failed",
            content: readString(error, "message") ?? readable(error),
            status: "Failed",
            tone: "error",
            routine: false,
          }
          break
        case "attempt.suspended_v1":
          entry = {
            ...entry,
            title: "Execution paused",
            status: "Waiting",
            tone: "waiting",
          }
          break
        case "attempt.cancelled_v1":
          entry = {
            ...entry,
            title: "Execution cancelled",
            status: "Cancelled",
            tone: "neutral",
          }
          break
      }
      entry.taskIDs = [
        ...new Set([
          ...entry.taskIDs,
          ...[entry.from, entry.to]
            .filter((a): a is TraceActor => a?.kind === "agent")
            .map((a) => a.id),
        ]),
      ]
      return entry
    })
}
