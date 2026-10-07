import { describe, expect, it } from "vitest"
import { buildTrace, traceTone } from "./run-trace"
import type { RunDetails, RuntimeEvent } from "./runtime"

const detail = {
  run: { lead_session_id: "lead" },
  tasks: [],
  turns: [],
} as unknown as RunDetails
function event(
  position: number,
  type: string,
  payload: Record<string, unknown> = {},
  session = "lead",
  task?: string,
): RuntimeEvent {
  return {
    position,
    event_id: `event-${position}`,
    type,
    payload,
    session_id: session,
    entity_id: (payload.session_id as string) ?? "entity",
    task_id: task,
  }
}
const delegation = event(2, "task.created_v1", {
  task_id: "code",
  session_id: "code-session",
  agent_id: "blix_code_v1",
  objective: "Fix pagination",
})

describe("trace communication routes", () => {
  it("routes user messages, delegation, clarification, and results to the correct participants", () => {
    const entries = buildTrace(
      [
        event(1, "message.user_v1", { content: "Fix pagination" }),
        delegation,
        event(
          3,
          "runtime.question_v1",
          {
            questions: [
              { question: "Which page size?", options: [{ label: "24" }] },
            ],
          },
          "code-session",
          "code",
        ),
        event(
          4,
          "task.answered_v1",
          { answers: [{ selected_options: ["24"] }] },
          "code-session",
          "code",
        ),
        event(
          5,
          "runtime.coding_trace_v1",
          { kind: "answer_delivered", content: "Answer sent" },
          "code-session",
          "code",
        ),
        event(
          6,
          "task.result_v1",
          {
            task_id: "code",
            status: "succeeded",
            result: { output: { summary: "Draft PR created" } },
          },
          "lead",
          "code",
        ),
        event(7, "message.assistant_v1", { content: "Done" }),
      ],
      detail,
    )
    expect(entries[0].from.name).toBe("User")
    expect(entries[0].to?.name).toBe("Blix lead")
    expect(entries[1].to?.name).toBe("blix_code_v1")
    expect(entries[2].from.id).toBe("code")
    expect(entries[2].to?.id).toBe("lead")
    expect(entries[2].content).toContain("24")
    expect(entries[3].from.id).toBe("lead")
    expect(entries[3].to?.id).toBe("code")
    expect(entries[3].status).toBe("Delivery pending")
    expect(entries[4].status).toBe("Delivered to blix_code_v1")
    expect(entries[5].from.id).toBe("code")
    expect(entries[5].to?.id).toBe("lead")
    expect(entries[5].content).toBe("Draft PR created")
    expect(entries[6].to?.name).toBe("User")
  })
  it("resolves nested agents without presenting the lead as the caller", () => {
    const nested = event(
      3,
      "task.created_v1",
      {
        task_id: "research",
        session_id: "research-session",
        agent_id: "deep_research_v1",
      },
      "code-session",
      "code",
    )
    const result = event(
      4,
      "task.result_v1",
      {
        task_id: "research",
        status: "failed",
        result: { error: { message: "Upstream unavailable" } },
      },
      "code-session",
      "research",
    )
    const entries = buildTrace([result, delegation, nested], detail)
    expect(entries[1].from.id).toBe("code")
    expect(entries[1].taskIDs).toEqual(
      expect.arrayContaining(["code", "research"]),
    )
    expect(entries[2].to?.id).toBe("code")
    expect(entries[2].tone).toBe("error")
    expect(entries[2].content).toBe("Upstream unavailable")
  })
  it("keeps model failures visible while suppressing routine token deltas", () => {
    const entries = buildTrace(
      [
        event(1, "model.output.delta_v1", { delta: "Partial output" }),
        event(2, "model.failure_v1", { error: { message: "Rate limited" } }),
      ],
      detail,
    )
    expect(entries[0].routine).toBe(true)
    expect(entries[1].routine).toBe(false)
    expect(entries[1].tone).toBe("error")
  })
  it("correlates tool results with their tool call", () => {
    const entries = buildTrace(
      [
        event(1, "tool.call_v1", {
          tool_id: "agent_execution_v1",
          arguments: { operation: "status" },
        }),
        event(2, "tool.result_v1", {
          status: "failed",
          error: { message: "Task unavailable" },
        }),
      ],
      detail,
    )
    expect(entries[1].from.name).toBe("agent_execution_v1")
    expect(entries[1].to?.name).toBe("Blix lead")
    expect(entries[1].tone).toBe("error")
  })
  it("does not invent a sender for an unknown session", () => {
    expect(
      buildTrace([event(1, "runtime.progress_v1", {}, "unknown")], detail)[0]
        .from.name,
    ).toBe("Runtime")
  })
  it("classifies status without treating cancellation as a failure", () => {
    expect(traceTone("waiting_for_input")).toBe("waiting")
    expect(traceTone("succeeded")).toBe("success")
    expect(traceTone("cancelled")).toBe("neutral")
  })
})
