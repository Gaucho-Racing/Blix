import { beforeEach, describe, expect, it, vi } from "vitest"
import { api } from "@/lib/api"
import { liveOutputs, loadEvents, streamedMessage } from "@/lib/runtime"
import type { ConversationDetails, RuntimeEvent } from "@/lib/runtime"

vi.mock("@/lib/api", () => ({ api: { get: vi.fn() } }))
const event = (
  position: number,
  type: string,
  delta = "",
  task?: string,
): RuntimeEvent => ({
  position,
  event_id: `event-${position}`,
  session_id: "lead",
  turn_id: "turn-1",
  task_id: task,
  type,
  entity_id: "call",
  payload: { delta },
})
const details: ConversationDetails = {
  conversation: {
    kind: "chat",
    document_ids: [],
    conversation_id: "conv",
    name: "",
    status: "active",
    lead_session_id: "lead",
    created_at: "",
    updated_at: "",
  },
  messages: [],
  turns: [{ turn_id: "turn-1", status: "running" }],
  tasks: [],
  pending_approvals: [],
  approval_actions: [],
}

describe("live output", () => {
  it("resets per model call and excludes executor text", () => {
    const events = [
      event(1, "model.call_v1"),
      event(2, "model.output.delta_v1", "intermediate"),
      event(3, "model.call_v1"),
      event(4, "model.output.delta_v1", "final"),
      event(5, "model.output.delta_v1", " answer"),
      event(6, "model.output.delta_v1", "private executor text", "task"),
    ]
    expect(liveOutputs(events, details)).toEqual([["turn-1", "final answer"]])
  })
  it("does not duplicate a persisted response or replay completed turns", () => {
    const events = [event(1, "model.output.delta_v1", "answer")]
    expect(
      liveOutputs(events, {
        ...details,
        messages: [
          {
            message_id: "reply",
            turn_id: "turn-1",
            role: "assistant",
            content: "answer",
            event_position: 2,
          },
        ],
      }),
    ).toEqual([])
    expect(
      liveOutputs(events, {
        ...details,
        turns: [{ turn_id: "turn-1", status: "succeeded" }],
      }),
    ).toEqual([])
  })
})
describe("event polling", () => {
  beforeEach(() => vi.mocked(api.get).mockReset())
  it("keeps unread background events when a newer user message arrives", async () => {
    const previous = [event(5, "message.created_v1")]
    const reply = event(6, "message.created_v1")
    const newerInput = event(7, "message.created_v1")
    vi.mocked(api.get).mockResolvedValueOnce({
      data: { events: [reply, newerInput] },
    })
    expect(await loadEvents("conv", previous)).toEqual([
      ...previous,
      reply,
      newerInput,
    ])
    expect(api.get).toHaveBeenCalledWith("/v1/conversations/conv/events", {
      params: { after: 5 },
      signal: undefined,
    })
  })
  it("drains full batches and avoids repeating overlapping events", async () => {
    const first = Array.from({ length: 100 }, (_, i) =>
      event(i + 1, "model.call_v1"),
    )
    vi.mocked(api.get)
      .mockResolvedValueOnce({ data: { events: first } })
      .mockResolvedValueOnce({
        data: { events: [first[99], event(101, "message.created_v1")] },
      })
    expect((await loadEvents("conv", [])).length).toBe(101)
    expect(api.get).toHaveBeenLastCalledWith("/v1/conversations/conv/events", {
      params: { after: 100 },
      signal: undefined,
    })
  })
})

describe("structured response streaming", () => {
  it("shows only the message and decodes escaped newlines and quotes", () => {
    expect(
      streamedMessage(
        JSON.stringify({ message: 'A line\nA "quote" and \\ path' }),
      ),
    ).toBe('A line\nA "quote" and \\ path')
  })
  it("waits for partial field names and incomplete unicode escapes", () => {
    expect(streamedMessage('{"mess')).toBe("")
    expect(streamedMessage('{"message":"Hello \\u00')).toBe("Hello ")
    expect(streamedMessage('{"message":"Hello \\u00e9')).toBe("Hello é")
  })
})
