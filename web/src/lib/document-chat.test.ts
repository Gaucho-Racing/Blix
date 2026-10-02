import { beforeEach, describe, expect, it, vi } from "vitest"
import { api } from "@/lib/api"
import { createDocumentChat, getDocumentChat } from "@/lib/document-chat"

vi.mock("@/lib/api", () => ({ api: { get: vi.fn(), post: vi.fn() } }))
beforeEach(() => vi.clearAllMocks())

describe("document Q&A conversation", () => {
  it("loads the caller's existing document chat with cancellable requests", async () => {
    const conversation = { conversation_id: "qa", document_ids: ["document_rules"] }
    vi.mocked(api.get).mockResolvedValueOnce({ data: { conversation } })
    const controller = new AbortController()
    expect(await getDocumentChat("document_rules", controller.signal)).toBe(conversation)
    expect(api.get).toHaveBeenCalledWith("/v1/document-chats/document_rules", { signal: controller.signal })
    expect(api.post).not.toHaveBeenCalled()
  })

  it("uses the dedicated server-selected document chat endpoint when starting Q&A", async () => {
    const conversation = { conversation_id: "qa", document_ids: ["document_rules"] }
    vi.mocked(api.post).mockResolvedValueOnce({ data: { conversation } })
    expect(await createDocumentChat("document_rules")).toBe(conversation)
    expect(api.post).toHaveBeenCalledWith("/v1/document-chats/document_rules")
  })
})
