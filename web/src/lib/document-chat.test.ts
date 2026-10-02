import { beforeEach, describe, expect, it, vi } from "vitest"
import { api } from "@/lib/api"
import { createDocumentChat, documentQuestionFailure } from "@/lib/document-chat"
import type { RuntimeEvent } from "@/lib/runtime"

vi.mock("@/lib/api", () => ({ api: { get: vi.fn(), post: vi.fn() } }))
beforeEach(() => vi.clearAllMocks())

describe("document Q&A run", () => {
  it("uses the dedicated server-selected document chat endpoint when starting Q&A", async () => {
    const run = { run_id: "qa", document_ids: ["document_rules"] }
    vi.mocked(api.post).mockResolvedValueOnce({ data: { run } })
    expect(await createDocumentChat("document_rules")).toBe(run)
    expect(api.post).toHaveBeenCalledWith("/v1/document-chats/document_rules")
  })
})

describe("document question failures", () => {
  const turn = { turn_id: "current", status: "failed" }
  const failure = (turnID: string, message: string, position: number): RuntimeEvent => ({
    event_id: `failure-${position}`, position, turn_id: turnID, session_id: "qa",
    type: "attempt.failed_v1", entity_id: "attempt",
    payload: { error: { code: "invalid_document_question", message } },
  })

  it("surfaces the latest validation reason for this question without leaking another turn's error", () => {
    const reason = "You can only ask questions about the current document. Ask a standalone question; greetings and follow-ups aren’t supported."
    expect(documentQuestionFailure(turn, [
      failure("current", "Earlier attempt", 1), failure("current", reason, 2),
      failure("other", "Unrelated error", 3),
    ])).toEqual({ title: "Invalid question", message: reason })
  })

  it("keeps failures visible before the final event is fetched and ignores failed retry attempts while running", () => {
    expect(documentQuestionFailure(turn, [])?.message).toContain("standalone question")
    expect(documentQuestionFailure({ ...turn, status: "running" }, [failure("current", "Retrying", 1)])).toBeNull()
    expect(documentQuestionFailure({ ...turn, status: "succeeded" }, [failure("current", "Earlier failure", 1)])).toBeNull()
  })

  it("shows the existing greeting validation clearly without relabeling search failures", () => {
    const event = failure("current", "", 1)
    event.payload.error = { code: "invalid_agent_output", message: "Document Q&A must search original document evidence before answering." }
    expect(documentQuestionFailure(turn, [event])?.title).toBe("Invalid question")
    event.payload.error = { code: "invalid_agent_output", message: "Blix couldn’t search this document. Try your question again." }
    expect(documentQuestionFailure(turn, [event])).toEqual({
      title: "Question not answered", message: "Blix couldn’t search this document. Try your question again.",
    })
  })
})
