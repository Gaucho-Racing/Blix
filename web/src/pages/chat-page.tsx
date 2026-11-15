import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowUp,
  Check,
  LoaderCircle,
  Plus,
  Square,
  X,
} from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { Link, useLocation, useNavigate, useParams } from "react-router-dom"
import { ChatMessage } from "@/components/chat-message"
import { useAuth } from "@/lib/auth"
import { useIdentities } from "@/lib/identity"
import {
  activeStatus,
  appendMessage,
  createRun,
  decideApproval,
  errorMessage,
  getRun,
  interruptRun,
  listChatRuns,
  liveOutputs,
  loadEvents,
} from "@/lib/runtime"
import type { RuntimeEvent, RuntimeTask } from "@/lib/runtime"

function TaskCard({ task }: { task: RuntimeTask }) {
  const output = task.result?.output
  const summary =
    typeof output?.summary === "string"
      ? output.summary
      : typeof output?.report === "string"
        ? output.report
        : ""
  const changesets = Array.isArray(output?.changesets)
    ? (output.changesets as { changeset_id: string; status: string }[])
    : []
  return (
    <article className="runtime-task">
      <div className="runtime-task-heading">
        <span className={`runtime-task-status status-${task.status}`}>
          {task.status}
        </span>
        <small>{task.background ? "Background" : "Delegated"}</small>
      </div>
      <strong>
        {task.objective.length > 180
          ? `${task.objective.slice(0, 180)}…`
          : task.objective}
      </strong>
      {task.objective.length > 180 && (
        <details className="runtime-task-instructions">
          <summary>Full task instructions</summary>
          <p>{task.objective}</p>
        </details>
      )}
      {summary && (
        <details>
          <summary>View findings</summary>
          <ChatMessage role="assistant" content={summary} />
        </details>
      )}
      {changesets.map((change) => (
        <Link
          className="runtime-proposal-link"
          key={change.changeset_id}
          to={`/knowledge/proposals/${change.changeset_id}`}
        >
          View memory proposal · {change.status}
        </Link>
      ))}
      {Array.isArray(output?.warnings) && output.warnings.length > 0 && (
        <details>
          <summary>Uncertainties & warnings</summary>
          <ul>
            {output.warnings
              .filter(
                (warning): warning is string => typeof warning === "string",
              )
              .map((warning, index) => (
                <li key={index}>{warning}</li>
              ))}
          </ul>
        </details>
      )}
      {task.error?.message && (
        <p role="alert" className="runtime-error">
          {task.error.message}
        </p>
      )}
    </article>
  )
}

export function ChatPage() {
  const { runID = "" } = useParams()
  return <RunChat key={runID || "new"} runID={runID} />
}

const activityTypes = new Set([
  "tool.call_v1",
  "tool.result_v1",
  "task.created_v1",
  "task.result_v1",
  "runtime.progress_v1",
  "model.failure_v1",
  "approval.decided_v1",
  "attempt.lease_expired_v1",
  "attempt.failed_v1",
])

function readableID(value: unknown, fallback: string) {
  return typeof value === "string" && value
    ? value.replace(/_v\d+$/, "").replaceAll("_", " ")
    : fallback
}

function activityLabel(event: RuntimeEvent): [string, string] {
  const payload = event.payload
  const status = typeof payload.status === "string" ? payload.status.replaceAll("_", " ") : ""
  switch (event.type) {
    case "tool.call_v1":
      return [readableID(payload.tool_id, "tool"), "started"]
    case "tool.result_v1":
      return [readableID(payload.tool_id, "tool"), status || "finished"]
    case "task.created_v1":
      return [readableID(payload.agent_id, "agent"), payload.background === true ? "started in background" : "started"]
    case "task.result_v1":
      return [readableID(payload.agent_id, "agent"), status || "finished"]
    case "runtime.progress_v1":
      return ["coding agent", status || "working"]
    case "model.failure_v1":
      return [
        "model call",
        typeof payload.retry_in_seconds === "number"
          ? `retrying in ${Math.round(payload.retry_in_seconds)}s`
          : "failed",
      ]
    case "approval.decided_v1":
      return ["approval", status || "decided"]
    case "attempt.lease_expired_v1":
      return ["agent attempt", "requeued"]
    case "attempt.failed_v1":
      return ["agent attempt", "failed"]
    default:
      return [event.type, status]
  }
}

function RunChat({ runID }: { runID: string }) {
  const auth = useAuth()
  const navigate = useNavigate()
  const { hash } = useLocation()
  const focusedMessage = useRef("")
  const cache = useQueryClient()
  const [draft, setDraft] = useState("")
  const pendingRun = useRef("")
  const end = useRef<HTMLDivElement>(null)
  const scroll = useRef<HTMLDivElement>(null)
  const [follow, setFollow] = useState(!hash)
  const permitted =
    auth.isAuthenticated && (auth.user?.groups.includes("BlixUsers") ?? false)
  const recent = useQuery({
    queryKey: ["runs"],
    queryFn: listChatRuns,
    enabled: permitted,
    refetchInterval: 10_000,
  })
  const detail = useQuery({
    queryKey: ["run", runID],
    queryFn: ({ signal }) => getRun(runID, signal),
    enabled: permitted && !!runID,
    refetchInterval: 1500,
  })
  const eventQuery = useQuery({
    queryKey: ["run-events", runID],
    queryFn: ({ signal }) =>
      loadEvents(
        runID,
        cache.getQueryData<RuntimeEvent[]>([
          "run-events",
          runID,
        ]) ?? [],
        signal,
      ),
    enabled: permitted && !!runID,
    refetchInterval: 1000,
  })
  const data = detail.data
  const runOwner = data?.run.caller_entity_id ?? ""
  const ownRun = !runOwner || runOwner === auth.user?.entity_id
  const owner = useIdentities(ownRun ? [] : [runOwner])
  const ownerIdentity = owner.identities.find((identity) => identity.id === runOwner)
  const authorAvatarURL = ownRun ? auth.user?.avatar_url : ownerIdentity?.avatar_url
  const authorInitials = (ownerIdentity?.name ?? "")
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0])
    .join("")
    .toUpperCase() || "?"
  const events = eventQuery.data ?? []
  const streaming = liveOutputs(events, data)
  const working = data?.turns.some((turn) => activeStatus(turn.status)) ?? false
  const refresh = async () => {
    await Promise.all([
      cache.invalidateQueries({ queryKey: ["run", runID] }),
      cache.invalidateQueries({
        queryKey: ["run-events", runID],
      }),
      cache.invalidateQueries({ queryKey: ["runs"] }),
    ])
  }
  const send = useMutation({
    mutationFn: async (content: string) => {
      if (!runID && !pendingRun.current) {
        pendingRun.current = (await createRun()).run_id
      }
      const id = runID || pendingRun.current
      await appendMessage(id, content)
      return id
    },
    onSuccess: async (id) => {
      setDraft("")
      await cache.invalidateQueries({ queryKey: ["run", id] })
      await cache.invalidateQueries({ queryKey: ["runs"] })
      if (id !== runID) navigate(`/chat/${id}`)
    },
  })
  const stop = useMutation({
    mutationFn: () => interruptRun(runID),
    onSuccess: refresh,
  })
  const approval = useMutation({
    mutationFn: ({
      id,
      decision,
    }: {
      id: string
      decision: "approve" | "reject"
    }) => decideApproval(id, decision),
    onSuccess: refresh,
  })
  const activity = events
    .filter((event) => activityTypes.has(event.type))
    .slice(-8)
  useEffect(() => {
    if (hash && focusedMessage.current !== hash) {
      const target = document.getElementById(hash.slice(1))
      if (target) {
        focusedMessage.current = hash
        target.scrollIntoView({ block: "start" })
        return
      }
    }
    if (follow)
      end.current?.scrollIntoView({ behavior: "smooth", block: "end" })
  }, [data?.messages.length, events.length, follow, hash])
  return (
    <div className="workspace-page chat-page">
      <header className="workspace-toolbar chat-toolbar">
        <div>
          <span className="workspace-kicker">BLIX WORKSPACE</span>
          <h1>{data?.run.name || "What should we work on?"}</h1>
          <p>
            {data?.run.context?.source === "discord"
              ? "This run is linked to Discord. Replies are also sent to its channel or thread."
              : "Start a conversation. See where it takes you."}
          </p>
        </div>
        <Link className="secondary-button" to="/chat">
          <Plus size={16} /> New run
        </Link>
      </header>
      <div className="chat-layout">
        <main className="chat-stage">
          <div
            ref={scroll}
            className="chat-scroll"
            onScroll={() => {
              const element = scroll.current
              if (element)
                setFollow(
                  element.scrollHeight -
                    element.scrollTop -
                    element.clientHeight <
                    140,
                )
            }}
          >
            {!auth.isAuthenticated && !auth.isLoading && (
              <div className="chat-gate">
                <h2>Sign in to chat with Blix</h2>
                <Link className="primary-button" to="/auth/login">
                  Continue with Sentinel
                </Link>
              </div>
            )}
            {auth.isAuthenticated && !permitted && (
              <p role="alert">You need membership in BlixUsers to chat.</p>
            )}
            {(auth.isLoading ||
              (permitted && runID && detail.isPending)) && (
              <p>
                <LoaderCircle className="spin" size={18} /> Loading
                run…
              </p>
            )}
            {permitted && !runID && (
              <section className="chat-welcome">
                <img className="blix-spark" src="/blix-logo.png" alt="" width={56} height={56} />
                <h2>Gaucho Racing, connected.</h2>
                <p>
                  A shared space for ideas, questions, and whatever comes next.
                </p>
                <div className="prompt-grid">
                  {[
                    "Lorem ipsum dolor sit amet.",
                    "Consectetur adipiscing elit.",
                    "Sed do eiusmod tempor incididunt.",
                  ].map((prompt) => (
                    <button
                      type="button"
                      className="prompt-card"
                      onClick={() => setDraft(prompt)}
                      key={prompt}
                    >
                      {prompt}
                    </button>
                  ))}
                </div>
              </section>
            )}
            {data?.messages.map((message) => (
              <ChatMessage
                key={message.message_id}
                messageID={message.message_id}
                role={message.role}
                content={message.display_content || message.content}
                userAvatarURL={authorAvatarURL}
                userLabel={ownRun ? "You" : authorInitials}
              />
            ))}
            {streaming.map(([id, content]) => (
              <div className="runtime-stream" key={id}>
                <ChatMessage role="assistant" content={content} />
              </div>
            ))}
            {working && !streaming.length && (
              <p className="runtime-working">
                <LoaderCircle className="spin" size={15} /> Blix is working. You
                can send another question.
              </p>
            )}
            {data?.turns
              .filter((turn) =>
                ["failed", "cancelled", "expired"].includes(turn.status),
              )
              .slice(-3)
              .map((turn) => (
                <p className="runtime-error" key={turn.turn_id}>
                  A request {turn.status}. You can send a follow-up to continue.
                </p>
              ))}
            {(detail.error || eventQuery.error) && (
              <p role="alert" className="runtime-error">
                {errorMessage(detail.error || eventQuery.error)}{" "}
                <button type="button" onClick={() => void refresh()}>
                  Retry
                </button>
              </p>
            )}
            <div ref={end} />
          </div>
          <form
            className="runtime-composer"
            onSubmit={(event) => {
              event.preventDefault()
              if (draft.trim() && permitted && !send.isPending)
                send.mutate(draft.trim())
            }}
          >
            {(send.error || stop.error) && (
              <p role="alert" className="runtime-error">
                {errorMessage(send.error || stop.error)}
              </p>
            )}
            <textarea
              aria-label="Message Blix"
              disabled={
                !permitted || send.isPending || (!!runID && !data)
              }
              placeholder="Ask Blix anything…"
              rows={3}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (
                  event.key === "Enter" &&
                  !event.shiftKey &&
                  !event.nativeEvent.isComposing
                ) {
                  event.preventDefault()
                  event.currentTarget.form?.requestSubmit()
                }
              }}
            />
            <div>
              {working && (
                <button
                  className="secondary-button"
                  disabled={stop.isPending}
                  onClick={() => stop.mutate()}
                  type="button"
                >
                  <Square size={13} /> Stop
                </button>
              )}
              <button
                aria-label="Send message"
                className="primary-button"
                disabled={
                  !permitted ||
                  !draft.trim() ||
                  send.isPending ||
                  (!!runID && !data)
                }
                type="submit"
              >
                <ArrowUp size={17} />
              </button>
            </div>
          </form>
        </main>
        <aside className="runtime-sidebar">
          {!!data?.pending_approvals.length && (
            <section>
              <h2>Needs your approval</h2>
              {data.pending_approvals.map((item) => {
                const action = data.approval_actions?.find(
                  (action) => action.action_id === item.action_id,
                )
                return (
                  <article className="runtime-task" key={item.approval_id}>
                    <strong>
                      {action?.tool_id === "knowledge_apply_v1"
                        ? "Publish memory changes"
                        : "Approve an action"}
                    </strong>
                    {typeof action?.arguments.changeset_id === "string" && (
                      <Link
                        className="runtime-proposal-link"
                        to={`/knowledge/proposals/${action.arguments.changeset_id}`}
                      >
                        Review proposed changes
                      </Link>
                    )}
                    {action && (
                      <details className="runtime-approval-details">
                        <summary>
                          {action.tool_id
                            .replaceAll("_v1", "")
                            .replaceAll("_", " ")}{" "}
                          · action details
                        </summary>
                        <pre>{JSON.stringify(action.arguments, null, 2)}</pre>
                      </details>
                    )}
                    <div className="runtime-controls">
                      <button
                        disabled={approval.isPending || !action}
                        onClick={() =>
                          approval.mutate({
                            id: item.approval_id,
                            decision: "approve",
                          })
                        }
                        type="button"
                      >
                        <Check size={13} /> Approve
                      </button>
                      <button
                        disabled={approval.isPending}
                        onClick={() =>
                          approval.mutate({
                            id: item.approval_id,
                            decision: "reject",
                          })
                        }
                        type="button"
                      >
                        <X size={13} /> Reject
                      </button>
                    </div>
                  </article>
                )
              })}
              {approval.error && (
                <p role="alert" className="runtime-error">
                  {errorMessage(approval.error)}
                </p>
              )}
            </section>
          )}
          <section>
            <h2>
              Tasks <span>{data?.tasks.length ?? 0}</span>
            </h2>
            {data?.tasks.length ? (
              data.tasks.map((task) => (
                <TaskCard key={task.task_id} task={task} />
              ))
            ) : (
              <p>Delegated research and curation will appear here.</p>
            )}
          </section>
          {!!activity.length && (
            <details>
              <summary>Recent activity</summary>
              <ul className="runtime-activity">
                {activity.map((event) => {
                  const [label, status] = activityLabel(event)
                  return (
                    <li key={event.event_id}>
                      {label} <small>{status}</small>
                    </li>
                  )
                })}
              </ul>
            </details>
          )}
          <section>
            <h2>Recent runs</h2>
            {recent.data?.slice(0, 12).map((run) => (
              <Link
                className={`runtime-run ${runID === run.run_id ? "selected" : ""}`}
                key={run.run_id}
                to={`/chat/${run.run_id}`}
              >
                {run.name || "New run"}
              </Link>
            ))}
            {recent.error && (
              <p className="runtime-error">Could not load history.</p>
            )}
          </section>
        </aside>
      </div>
    </div>
  )
}
