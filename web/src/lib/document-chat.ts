import { api } from "@/lib/api"
import type { Conversation } from "@/lib/runtime"

export async function getDocumentChat(documentID: string, signal?: AbortSignal) {
  return (
    await api.get<{ conversation: Conversation | null }>(
      `/v1/document-chats/${documentID}`, { signal },
    )
  ).data.conversation
}

export async function createDocumentChat(documentID: string) {
  return (
    await api.post<{ conversation: Conversation }>(`/v1/document-chats/${documentID}`)
  ).data.conversation
}
