import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowUp,
  BookOpen,
  Check,
  LoaderCircle,
  Plus,
  Square,
  X,
} from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { Link, useNavigate, useParams } from "react-router-dom"
import { ChatMessage } from "@/components/chat-message"
import { ChatDocumentContext } from "@/components/chat-document-context"
import { useAuth } from "@/lib/auth"
import {
  activeStatus,
  appendMessage,
  controlTask,
  createConversation,
  decideApproval,
  errorMessage,
  getConversation,
  interruptConversation,
  listConversations,
  liveOutputs,
  loadEvents,
} from "@/lib/runtime"
import type { RuntimeEvent, RuntimeTask } from "@/lib/runtime"

function TaskCard({
  task,
  refresh,
}: {
  task: RuntimeTask
  refresh: () => Promise<void>
}) {
  const [direction, setDirection] = useState("")
  const mutation = useMutation({
    mutationFn: ({
      operation,
      content,
    }: {
      operation: "cancel" | "steer"
      content?: string
    }) => controlTask(task.task_id, operation, content),
    onSuccess: refresh,
  })
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
      {activeStatus(task.status) && (
        <form
          onSubmit={(event) => {
            event.preventDefault()
            if (direction.trim())
              mutation.mutate({ operation: "steer", content: direction })
          }}
        >
          <input
            aria-label="Task direction"
            onChange={(event) => setDirection(event.target.value)}
            placeholder="Give this task direction…"
            value={direction}
          />
          <div className="runtime-controls">
            <button
              disabled={mutation.isPending || !direction.trim()}
              type="submit"
            >
              Send direction
            </button>
            <button
              disabled={mutation.isPending}
              onClick={() => mutation.mutate({ operation: "cancel" })}
              type="button"
            >
              Cancel task
            </button>
          </div>
        </form>
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
          View wiki proposal · {change.status}
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
      {mutation.error && (
        <p role="alert" className="runtime-error">
          {errorMessage(mutation.error)}
        </p>
      )}
    </article>
  )
}

export function ChatPage() {
  const { runID = "" } = useParams()
  return <ConversationChat key={runID || "new"} conversationID={runID} />
}

function ConversationChat({ conversationID }: { conversationID: string }) {
  const auth = useAuth()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [draft, setDraft] = useState("")
  const [documentIDs, setDocumentIDs] = useState<string[]>([])
  const [scopeLocked, setScopeLocked] = useState(false)
  const pendingConversation = useRef("")
  const end = useRef<HTMLDivElement>(null)
  const scroll = useRef<HTMLDivElement>(null)
  const [follow, setFollow] = useState(true)
  const permitted =
    auth.isAuthenticated && (auth.user?.groups.includes("BlixUsers") ?? false)
  const recent = useQuery({
    queryKey: ["conversations"],
    queryFn: listConversations,
    enabled: permitted,
    refetchInterval: 10_000,
  })
  const detail = useQuery({
    queryKey: ["conversation", conversationID],
    queryFn: ({ signal }) => getConversation(conversationID, signal),
    enabled: permitted && !!conversationID,
    refetchInterval: 1500,
  })
  const eventQuery = useQuery({
    queryKey: ["conversation-events", conversationID],
    queryFn: ({ signal }) =>
      loadEvents(
        conversationID,
        cache.getQueryData<RuntimeEvent[]>([
          "conversation-events",
          conversationID,
        ]) ?? [],
        signal,
      ),
    enabled: permitted && !!conversationID,
    refetchInterval: 1000,
  })
  const data = detail.data
  const events = eventQuery.data ?? []
  const streaming = liveOutputs(events, data)
  const working = data?.turns.some((turn) => activeStatus(turn.status)) ?? false
  const refresh = async () => {
    await Promise.all([
      cache.invalidateQueries({ queryKey: ["conversation", conversationID] }),
      cache.invalidateQueries({
        queryKey: ["conversation-events", conversationID],
      }),
      cache.invalidateQueries({ queryKey: ["conversations"] }),
    ])
  }
  const send = useMutation({
    mutationFn: async (content: string) => {
      if (!conversationID && !pendingConversation.current) {
        pendingConversation.current = (
          await createConversation(documentIDs)
        ).conversation_id
        setScopeLocked(true)
      }
      const id = conversationID || pendingConversation.current
      await appendMessage(id, content)
      return id
    },
    onSuccess: async (id) => {
      setDraft("")
      await cache.invalidateQueries({ queryKey: ["conversation", id] })
      await cache.invalidateQueries({ queryKey: ["conversations"] })
      if (id !== conversationID) navigate(`/chat/${id}`)
    },
  })
  const stop = useMutation({
    mutationFn: () => interruptConversation(conversationID),
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
    .filter((event) =>
      [
        "tool.call_v1",
        "tool.result_v1",
        "task.created_v1",
        "attempt.failed_v1",
      ].includes(event.type),
    )
    .slice(-8)
  useEffect(() => {
    if (follow)
      end.current?.scrollIntoView({ behavior: "smooth", block: "end" })
  }, [data?.messages.length, events.length, follow])
  return (
    <div className="workspace-page chat-page">
      <header className="workspace-toolbar chat-toolbar">
        <div>
          <span className="workspace-kicker">BLIX WORKSPACE</span>
          <h1>{data?.conversation.name || "What should we work on?"}</h1>
          <p>Quick answers. Research. Team knowledge.</p>
        </div>
        <Link className="secondary-button" to="/chat">
          <Plus size={16} /> New conversation
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
              (permitted && conversationID && detail.isPending)) && (
              <p>
                <LoaderCircle className="spin" size={18} /> Loading
                conversation…
              </p>
            )}
            {permitted && !conversationID && (
              <section className="chat-welcome">
                <div className="blix-spark">B</div>
                <h2>Your team's work, connected.</h2>
                <p>
                  Ask about a design, investigate a question, or turn documents
                  into useful knowledge.
                </p>
                <div className="prompt-grid">
                  {[
                    "What do we know about GR26?",
                    "Research a focused engineering question and cite your sources.",
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
                <Link className="runtime-proposal-link" to="/knowledge">
                  Import a team document <BookOpen size={15} />
                </Link>
              </section>
            )}
            {data?.messages.map((message) => (
              <ChatMessage
                key={message.message_id}
                role={message.role}
                content={message.display_content || message.content}
                userAvatarURL={auth.user?.avatar_url}
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
            <ChatDocumentContext
              documentIDs={
                conversationID
                  ? (data?.conversation.document_ids ?? [])
                  : documentIDs
              }
              onChange={
                !conversationID && !scopeLocked && !send.isPending
                  ? setDocumentIDs
                  : undefined
              }
              disabled={!permitted}
            />
            {(send.error || stop.error) && (
              <p role="alert" className="runtime-error">
                {errorMessage(send.error || stop.error)}
              </p>
            )}
            <textarea
              aria-label="Message Blix"
              disabled={
                !permitted || send.isPending || (!!conversationID && !data)
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
              <Link to="/knowledge">Import PDF</Link>
              <span>Enter to send · Shift + Enter for a new line</span>
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
                  (!!conversationID && !data)
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
                        ? "Publish wiki changes"
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
                <TaskCard key={task.task_id} task={task} refresh={refresh} />
              ))
            ) : (
              <p>Delegated research and curation will appear here.</p>
            )}
          </section>
          {!!activity.length && (
            <details>
              <summary>Recent activity</summary>
              <ul className="runtime-activity">
                {activity.map((event) => (
                  <li key={event.event_id}>
                    {typeof event.payload.tool_id === "string"
                      ? event.payload.tool_id
                          .replaceAll("_v1", "")
                          .replaceAll("_", " ")
                      : event.type.split(".")[0]}{" "}
                    <small>
                      {typeof event.payload.status === "string"
                        ? event.payload.status
                        : event.type.includes("result")
                          ? "finished"
                          : "started"}
                    </small>
                  </li>
                ))}
              </ul>
            </details>
          )}
          <section>
            <h2>Recent conversations</h2>
            {recent.data?.slice(0, 12).map((conversation) => (
              <Link
                className={`runtime-conversation ${conversationID === conversation.conversation_id ? "selected" : ""}`}
                key={conversation.conversation_id}
                to={`/chat/${conversation.conversation_id}`}
              >
                {conversation.name || "New conversation"}
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
