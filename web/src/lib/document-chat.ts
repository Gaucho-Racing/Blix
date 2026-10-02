import { api } from "@/lib/api"
import type { Conversation, RuntimeEvent, RuntimeTurn } from "@/lib/runtime"

export function documentQuestionFailure(turn: RuntimeTurn | undefined, events: RuntimeEvent[]) {
  if (!turn) return null
  if (turn.status === "cancelled") return { title: "Answer stopped", message: "You can submit a new question." }
  if (turn.status === "expired") return { title: "Question timed out", message: "Try submitting a new question." }
  if (turn.status !== "failed") return null
  const failure = [...events].reverse().find((event) =>
    event.type === "attempt.failed_v1" && event.turn_id === turn.turn_id && !event.task_id,
  )
  const error = failure?.payload.error as { code?: unknown; message?: unknown } | undefined
  const invalid = error?.code === "invalid_document_question" ||
    (error?.code === "invalid_agent_output" && error.message === "Document Q&A must search original document evidence before answering.")
  return {
    title: invalid ? "Invalid question" : "Question not answered",
    message: invalid
      ? "You can only ask questions about the current document. Ask a standalone question; greetings and follow-ups aren’t supported."
      : typeof error?.message === "string" && error.message.trim()
        ? error.message
        : "Blix couldn’t answer this question. Try a standalone question about this document.",
  }
}

export async function createDocumentChat(documentID: string) {
  return (
    await api.post<{ conversation: Conversation }>(`/v1/document-chats/${documentID}`)
  ).data.conversation
}
