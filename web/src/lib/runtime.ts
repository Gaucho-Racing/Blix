import { api } from "@/lib/api"

export interface Conversation {
  document_ids: string[]
  conversation_id: string
  name: string
  status: string
  lead_session_id: string
  updated_at: string
  created_at: string
}
export interface RuntimeMessage {
  message_id: string
  turn_id: string
  role: "user" | "assistant"
  content: string
  display_content?: string
  event_position: number
}
export interface RuntimeTurn {
  turn_id: string
  status: string
  input_message_id?: string
}
export interface RuntimeTask {
  task_id: string
  objective: string
  status: string
  background: boolean
  result?: { output?: Record<string, unknown> }
  error?: { message?: string }
  created_at: string
}
export interface RuntimeEvent {
  position: number
  event_id: string
  turn_id?: string
  task_id?: string
  session_id: string
  type: string
  entity_id: string
  payload: Record<string, unknown>
}
export interface ApprovalAction {
  action_id: string
  tool_id: string
  arguments: Record<string, unknown>
}
export interface ConversationDetails {
  conversation: Conversation
  messages: RuntimeMessage[]
  turns: RuntimeTurn[]
  tasks: RuntimeTask[]
  pending_approvals: { approval_id: string; action_id: string }[]
  approval_actions: ApprovalAction[]
}

export const activeStatus = (status: string) =>
  ["queued", "running", "waiting", "suspended"].includes(status)
export const errorMessage = (error: unknown) =>
  (error as { response?: { data?: { error?: string } } })?.response?.data
    ?.error ??
  (error instanceof Error ? error.message : "Request failed. Try again.")
export async function listConversations() {
  return (await api.get<{ conversations: Conversation[] }>("/v1/conversations"))
    .data.conversations
}
export async function createConversation(documentIDs: string[] = []) {
  return (
    await api.post<{ conversation: Conversation }>("/v1/conversations", {
      document_ids: documentIDs,
    })
  ).data.conversation
}
export async function getConversation(id: string, signal?: AbortSignal) {
  return (
    await api.get<ConversationDetails>(`/v1/conversations/${id}`, { signal })
  ).data
}
export async function appendMessage(
  id: string,
  content: string,
  displayContent?: string,
) {
  return (
    await api.post<{ message: RuntimeMessage }>(
      `/v1/conversations/${id}/messages`,
      { content, display_content: displayContent },
    )
  ).data
}
export async function interruptConversation(id: string) {
  await api.post(`/v1/conversations/${id}/interrupt`)
}
export async function controlTask(
  id: string,
  operation: "cancel" | "steer",
  content = "",
) {
  await api.post(`/v1/tasks/${id}/${operation}`, { content })
}
export async function decideApproval(
  id: string,
  decision: "approve" | "reject",
  note = "",
) {
  await api.post(`/v1/approvals/${id}/${decision}`, { note })
}
export async function loadEvents(
  id: string,
  previous: RuntimeEvent[],
  signal?: AbortSignal,
) {
  const events = [...previous]
  let after = events.at(-1)?.position ?? 0
  while (true) {
    const batch = (
      await api.get<{ events: RuntimeEvent[] }>(
        `/v1/conversations/${id}/events`,
        { params: { after }, signal },
      )
    ).data.events
    const fresh = batch.filter((event) => event.position > after)
    events.push(...fresh)
    if (batch.length < 100 || !fresh.length) return events
    after = fresh.at(-1)!.position
  }
}
export function liveOutputs(
  events: RuntimeEvent[],
  details: ConversationDetails | undefined,
) {
  const output = new Map<string, string>()
  const finished = new Set(
    details?.messages
      .filter((message) => message.role === "assistant")
      .map((message) => message.turn_id),
  )
  const active = new Set(
    details?.turns
      .filter((turn) => activeStatus(turn.status))
      .map((turn) => turn.turn_id),
  )
  for (const event of events) {
    if (
      !event.turn_id ||
      event.task_id ||
      !active.has(event.turn_id) ||
      finished.has(event.turn_id)
    )
      continue
    if (event.type === "model.call_v1") output.set(event.turn_id, "")
    if (
      event.type === "model.output.delta_v1" &&
      typeof event.payload.delta === "string"
    )
      output.set(
        event.turn_id,
        (output.get(event.turn_id) ?? "") + event.payload.delta,
      )
  }
  return [...output.entries()]
    .map(([id, raw]): [string, string] => [id, streamedMessage(raw)])
    .filter(([, content]) => content)
}

export function streamedMessage(raw: string): string {
  if (!raw.trimStart().startsWith("{")) return raw
  const start = raw.match(/^\s*\{\s*"message"\s*:\s*"/)
  if (!start) return ""
  let text = ""
  const body = raw.slice(start[0].length)
  for (let index = 0; index < body.length; index++) {
    const character = body[index]
    if (character === '"') break
    if (character !== "\\") {
      text += character
      continue
    }
    const next = body[++index]
    if (next == null) break
    if (next === "u") {
      const hex = body.slice(index + 1, index + 5)
      if (!/^[0-9a-fA-F]{4}$/.test(hex)) break
      text += String.fromCharCode(Number.parseInt(hex, 16))
      index += 4
    } else {
      const escapes: Record<string, string> = {
        n: "\n",
        r: "\r",
        t: "\t",
        b: "\b",
        f: "\f",
        '"': '"',
        "\\": "\\",
        "/": "/",
      }
      if (!(next in escapes)) break
      text += escapes[next]
    }
  }
  return text
}
