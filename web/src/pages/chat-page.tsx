import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowUp,
  BookOpen,
  Clock3,
  FileSearch,
  Hash,
  LoaderCircle,
  LogIn,
  Plus,
  Sparkles,
  TriangleAlert,
} from "lucide-react"
import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react"
import type { FormEvent, KeyboardEvent } from "react"
import { Link, useLocation, useNavigate, useParams } from "react-router-dom"

import { ChatActivity } from "@/components/chat-activity"
import { RunEventStream } from "@/components/event-stream"
import { StatusBadge } from "@/components/status-badge"
import { appendRunMessage, createRun, getRun, getRunEvents, listMyRuns } from "@/lib/api"
import { useAuth } from "@/lib/auth"
import { formatRelative, readString, runDisplayStatus } from "@/lib/format"
import type { ExecutionEvent, ExecutionSummary } from "@/lib/types"
import { useNow } from "@/lib/use-now"

const prompts = [
  {
    icon: BookOpen,
    label: "Explore Blix",
    prompt: "What can you help me with right now?",
  },
  {
    icon: FileSearch,
    label: "Deep research",
    prompt: "Research the biggest Formula SAE EV rule changes for 2026 and cite your sources.",
  },
  {
    icon: Clock3,
    label: "Use a tool",
    prompt: "What time is it in Tokyo right now?",
  },
]

const ChatMessage = lazy(() => import("@/components/chat-message").then((module) => ({ default: module.ChatMessage })))
const noEvents: ExecutionEvent[] = []
const noExecutions: ExecutionSummary[] = []

interface PendingSubmission {
  content: string
  runID?: string
  rootExecutionID?: string
  eventPosition?: number
}

interface ConversationTurn {
  user: ExecutionEvent
  assistant?: ExecutionEvent
  activity: ExecutionEvent[]
  failure?: ExecutionEvent
}

function conversationTurns(events: ExecutionEvent[], rootExecutionID: string): ConversationTurn[] {
  const sortedEvents = [...events].sort((left, right) => left.position - right.position)
  const rootMessages = sortedEvents.filter(
    (event) => event.execution_id === rootExecutionID &&
      (event.type === "message.user_v1" || event.type === "message.assistant_v1"),
  )

  return rootMessages.flatMap((event, index): ConversationTurn[] => {
    if (event.type !== "message.user_v1") return []
    const nextUser = rootMessages.slice(index + 1).find((candidate) => candidate.type === "message.user_v1")
    const assistant = rootMessages
      .slice(index + 1)
      .find((candidate) => candidate.type === "message.assistant_v1" && (!nextUser || candidate.position < nextUser.position))
    const endPosition = assistant?.position ?? nextUser?.position ?? Number.POSITIVE_INFINITY
    const activity = sortedEvents.filter(
      (candidate) => candidate.position > event.position &&
        candidate.position < endPosition &&
        candidate.type !== "message.user_v1",
    )
    const failure = activity.find((candidate) => candidate.type === "execution.failed_v1")
    return [{ user: event, assistant, activity, failure }]
  })
}

function eventContent(event: ExecutionEvent | undefined): string {
  return event ? readString(event.payload, "content") ?? "" : ""
}

function failureContent(event: ExecutionEvent | undefined): string {
  const error = event?.payload.error
  if (error !== null && typeof error === "object" && !Array.isArray(error)) {
    return readString(error as Record<string, unknown>, "message") ?? "The agent execution failed."
  }
  return "The agent execution failed. Start a new conversation to try again."
}

function requestErrorMessage(error: unknown): string {
  const responseMessage = (error as { response?: { data?: { error?: string } } })?.response?.data?.error
  if (responseMessage === "resource state conflict") {
    return "This conversation is still working on its previous message."
  }
  return responseMessage || "Blix could not send that message. Please try again."
}

function enteringRunFromLocationState(value: unknown): string {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return ""
  const enteringRunID = (value as Record<string, unknown>).enteringRunID
  return typeof enteringRunID === "string" ? enteringRunID : ""
}

export function ChatPage() {
  const { runID = "" } = useParams()
  const auth = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const now = useNow()
  const [draft, setDraft] = useState("")
  const [pendingSubmission, setPendingSubmission] = useState<PendingSubmission | null>(null)
  const [enteringRunID, setEnteringRunID] = useState(() => enteringRunFromLocationState(location.state))
  const conversationEnd = useRef<HTMLDivElement>(null)
  const hasBlixAccess = auth.user?.groups.includes("BlixUsers") ?? false
  const canChat = auth.isAuthenticated && hasBlixAccess

  const runQuery = useQuery({
    queryKey: ["run", runID],
    queryFn: () => getRun(runID),
    enabled: canChat && Boolean(runID),
  })
  const eventsQuery = useQuery({
    queryKey: ["run-events", runID],
    queryFn: () => getRunEvents(runID),
    enabled: canChat && Boolean(runID),
  })
  const recentRunsQuery = useQuery({
    queryKey: ["my-runs"],
    queryFn: listMyRuns,
    enabled: canChat,
  })

  const runDetails = runQuery.data
  const run = runDetails?.run
  const executions = runDetails?.executions ?? noExecutions
  const events = eventsQuery.data ?? noEvents
  const turns = useMemo(
    () => conversationTurns(events, run?.root_execution_id ?? ""),
    [events, run?.root_execution_id],
  )
  const rootExecution = executions.find((execution) => execution.execution_id === run?.root_execution_id)
  const executionInFlight = rootExecution
    ? ["queued", "running", "waiting"].includes(rootExecution.status)
    : false
  const executionFailed = rootExecution?.status === "failed"
  const pendingEventPosition = pendingSubmission?.eventPosition
  const pendingRootExecutionID = run?.root_execution_id ?? pendingSubmission?.rootExecutionID
  const pendingTerminalEvent = pendingEventPosition && pendingRootExecutionID
    ? events.find(
        (event) => event.position > pendingEventPosition &&
          (event.type === "message.assistant_v1" || event.type === "execution.failed_v1") &&
          event.execution_id === pendingRootExecutionID,
      )
    : undefined
  const pendingMatchesRun = !pendingSubmission?.runID || !runID || pendingSubmission.runID === runID
  const activePendingSubmission = pendingMatchesRun && !pendingTerminalEvent ? pendingSubmission : null
  const pendingEventPersisted = activePendingSubmission?.eventPosition
    ? events.some((event) => event.position === activePendingSubmission.eventPosition)
    : false
  const optimisticSubmission = activePendingSubmission && !pendingEventPersisted ? activePendingSubmission : null
  const currentTurn = turns.at(-1)
  const currentTurnFinished = Boolean(currentTurn?.assistant || currentTurn?.failure)
  const turnActive = Boolean(activePendingSubmission) || executionInFlight || Boolean(currentTurn && !currentTurnFinished)
  const composerDisabled = !canChat || Boolean(runID && (runQuery.isPending || runQuery.isError || executionFailed)) || turnActive

  useEffect(() => {
    if (turns.length === 0 && !optimisticSubmission) return
    const frame = window.requestAnimationFrame(() => conversationEnd.current?.scrollIntoView({ behavior: "smooth" }))
    return () => window.cancelAnimationFrame(frame)
  }, [events.length, optimisticSubmission, turns.length])

  useEffect(() => {
    if (!enteringRunID) return
    const timer = window.setTimeout(() => {
      setEnteringRunID("")
      if (enteringRunFromLocationState(location.state) === enteringRunID) {
        navigate(`${location.pathname}${location.search}`, { replace: true, state: null })
      }
    }, 1_400)
    return () => window.clearTimeout(timer)
  }, [enteringRunID, location.pathname, location.search, location.state, navigate])

  const sendMutation = useMutation({
    mutationFn: async (content: string) => {
      let targetRunID = runID
      let targetRootExecutionID = run?.root_execution_id
      const createdNewRun = !targetRunID
      if (!targetRunID) {
        const created = await createRun()
        targetRunID = created.run.run_id
        targetRootExecutionID = created.run.root_execution_id
      }
      const appended = await appendRunMessage(targetRunID, content)
      return { runID: targetRunID, rootExecutionID: targetRootExecutionID, appended, createdNewRun }
    },
    scope: { id: "chat-send" },
    onMutate: (content) => setPendingSubmission({ content, runID: runID || undefined, rootExecutionID: run?.root_execution_id }),
    onSuccess: ({ runID: targetRunID, rootExecutionID, appended, createdNewRun }, content) => {
      setPendingSubmission({ content, runID: targetRunID, rootExecutionID, eventPosition: appended.event_position })
      if (createdNewRun) setEnteringRunID(targetRunID)
      if (runID !== targetRunID) {
        navigate(`/chat/${targetRunID}`, {
          replace: true,
          state: createdNewRun ? { enteringRunID: targetRunID } : null,
        })
      }
      void Promise.all([
        queryClient.invalidateQueries({ queryKey: ["run", targetRunID] }),
        queryClient.invalidateQueries({ queryKey: ["run-events", targetRunID] }),
        queryClient.invalidateQueries({ queryKey: ["my-runs"] }),
      ])
    },
    onError: (_, content) => {
      setPendingSubmission(null)
      setDraft(content)
    },
  })

  function submitMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const message = draft.trim()
    if (!message) return
    if (!auth.isAuthenticated) {
      navigate("/auth/login", { state: { from: location } })
      return
    }
    if (!hasBlixAccess || composerDisabled) return
    setDraft("")
    sendMutation.mutate(message)
  }

  function handleComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return
    event.preventDefault()
    event.currentTarget.form?.requestSubmit()
  }

  function startNewConversation() {
    setDraft("")
    setPendingSubmission(null)
    sendMutation.reset()
    navigate("/chat")
  }

  const recentRuns = (recentRunsQuery.data ?? []).slice(0, 8)
  const displayName = auth.user?.first_name || auth.user?.username
  const showWelcome = turns.length === 0 && !optimisticSubmission

  return (
    <div className="workspace-page chat-page">
      <header className="workspace-toolbar chat-toolbar">
        <div>
          <span className="workspace-kicker">TEAM WORKSPACE</span>
          <h1>{run?.name || "Chat"}</h1>
          {run && <code>{run.run_id}</code>}
        </div>
        <div className="chat-toolbar-actions">
          {eventsQuery.data && run && (
            <RunEventStream
              initialCursor={events.at(-1)?.position ?? run.last_event_position}
              key={run.run_id}
              runID={run.run_id}
            />
          )}
          <button className="secondary-button" onClick={startNewConversation} type="button"><Plus size={16} /> New conversation</button>
        </div>
      </header>

      <div className="chat-layout">
        <main className="chat-stage">
          <div className="chat-scroll" aria-live="polite">
            {auth.isLoading ? (
              <div className="chat-gate"><LoaderCircle className="spin" size={20} /><strong>Loading your workspace</strong></div>
            ) : !auth.isAuthenticated ? (
              <div className="chat-gate chat-gate-signin">
                <div className="blix-spark"><Sparkles size={23} /></div>
                <span className="workspace-kicker">BLIX</span>
                <h2>Sign in to start a conversation</h2>
                <p>Use your Gaucho Racing Sentinel account to give Blix the right identity and permissions.</p>
                <button className="primary-button" onClick={() => navigate("/auth/login", { state: { from: location } })} type="button">
                  <LogIn size={15} /> Continue with Sentinel
                </button>
              </div>
            ) : !hasBlixAccess ? (
              <div className="chat-gate chat-gate-denied">
                <span className="chat-gate-icon"><TriangleAlert size={20} /></span>
                <h2>You do not have access to Blix</h2>
                <p>Please request to join the <strong>BlixUsers</strong> group in Sentinel.</p>
              </div>
            ) : runID && runQuery.isPending ? (
              <div className="chat-gate"><LoaderCircle className="spin" size={20} /><strong>Loading conversation</strong></div>
            ) : runID && runQuery.isError ? (
              <div className="chat-gate chat-gate-denied">
                <span className="chat-gate-icon"><TriangleAlert size={20} /></span>
                <h2>Conversation unavailable</h2>
                <p>It may have been removed, or it belongs to another member.</p>
                <button className="secondary-button" onClick={startNewConversation} type="button"><Plus size={15} /> Start a new conversation</button>
              </div>
            ) : showWelcome ? (
              <>
                <section className="chat-welcome">
                  <div className="blix-spark"><Sparkles size={23} /></div>
                  <span>BLIX</span>
                  <h2>What should we work on{displayName ? `, ${displayName}` : ""}?</h2>
                  <p>Ask a question, research a problem, or kick off work across Gaucho Racing.</p>
                </section>
                <section className="prompt-grid" aria-label="Suggested prompts">
                  {prompts.map(({ icon: Icon, label, prompt }) => (
                    <button className="prompt-card" key={label} onClick={() => setDraft(prompt)} type="button">
                      <Icon size={17} />
                      <span>{label}</span>
                      <p>{prompt}</p>
                    </button>
                  ))}
                </section>
              </>
            ) : (
              <Suspense fallback={<div className="chat-gate"><LoaderCircle className="spin" size={20} /><strong>Rendering conversation</strong></div>}>
                <section className="chat-conversation">
                  {turns.map((turn, index) => {
                    const isLast = index === turns.length - 1
                    return (
                      <div className="chat-turn" key={turn.user.event_id}>
                        <ChatMessage content={eventContent(turn.user)} role="user" userAvatarURL={auth.user?.avatar_url} />
                        <ChatActivity
                          active={isLast && turnActive && !turn.assistant && !turn.failure}
                          completedAt={turn.assistant?.created_at ?? turn.failure?.created_at}
                          events={turn.activity}
                          executions={executions}
                          startedAt={turn.user.created_at}
                        />
                        {turn.assistant && <ChatMessage content={eventContent(turn.assistant)} role="assistant" />}
                        {turn.failure && <div className="chat-response-error"><TriangleAlert size={15} /><span>{failureContent(turn.failure)}</span></div>}
                      </div>
                    )
                  })}
                  {optimisticSubmission && (
                    <div className="chat-turn">
                      <ChatMessage content={optimisticSubmission.content} role="user" userAvatarURL={auth.user?.avatar_url} />
                      <ChatActivity active events={[]} executions={executions} />
                    </div>
                  )}
                  <div ref={conversationEnd} />
                </section>
              </Suspense>
            )}
          </div>

          {canChat && !runQuery.isError && (
            <form className="prompt-composer chat-prompt-composer" onSubmit={submitMessage}>
              {sendMutation.isError && <div className="composer-error"><TriangleAlert size={13} /> {requestErrorMessage(sendMutation.error)}</div>}
              <textarea
                aria-label="Message Blix"
                disabled={composerDisabled}
                onChange={(event) => setDraft(event.currentTarget.value)}
                onKeyDown={handleComposerKeyDown}
                placeholder={executionFailed ? "This run failed — start a new conversation" : turnActive ? "Blix is working…" : "Ask Blix anything…"}
                rows={2}
                value={draft}
              />
              <div className="composer-footer">
                <span className="agent-mode"><Sparkles size={14} /> Agent mode</span>
                <div className="composer-hint">Enter to send · Shift Enter for a new line</div>
                <button aria-label="Send message" className="send-button" disabled={!draft.trim() || composerDisabled} type="submit"><ArrowUp size={18} /></button>
              </div>
            </form>
          )}
        </main>

        <aside className="recent-work-panel chat-history-panel">
          <div className="recent-work-heading">
            <div><Hash size={15} /><h2>Recent conversations</h2></div>
            {recentRunsQuery.isFetching && <LoaderCircle className="spin" size={14} />}
          </div>
          {recentRunsQuery.isError ? (
            <div className="recent-work-empty">Recent conversations could not be loaded.</div>
          ) : recentRuns.length === 0 ? (
            <div className="recent-work-empty">Your conversations will appear here.</div>
          ) : (
            <div className="recent-thread-list">
              {recentRuns.map((recentRun) => (
                <Link
                  className={`recent-thread${recentRun.run_id === runID ? " active" : ""}${recentRun.run_id === enteringRunID ? " entering" : ""}`}
                  key={recentRun.run_id}
                  to={`/chat/${recentRun.run_id}`}
                >
                  <span>{recentRun.name || "New conversation"}</span>
                  <small>{formatRelative(recentRun.last_activity_at)} · {recentRun.event_count} events</small>
                  <StatusBadge status={runDisplayStatus(recentRun, now)} />
                </Link>
              ))}
            </div>
          )}
          <div className="recent-work-footer"><span className="healthy-dot" /> Conversations are checkpointed through Runway</div>
        </aside>
      </div>
    </div>
  )
}
