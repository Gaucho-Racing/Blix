import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { AlertCircle, ArrowUp, LoaderCircle, Square } from "lucide-react"
import { useEffect, useLayoutEffect, useRef, useState } from "react"
import { ChatMessage } from "@/components/chat-message"
import { useAuth } from "@/lib/auth"
import { createDocumentChat, documentQuestionFailure } from "@/lib/document-chat"
import type { DocumentDetails } from "@/lib/documents"
import {
  activeStatus, appendMessage, errorMessage, getRun,
  interruptRun, liveOutputs, loadEvents,
} from "@/lib/runtime"
import type { RuntimeEvent } from "@/lib/runtime"

export function DocumentChat({ details }: { details: DocumentDetails }) {
  const { document } = details
  const auth = useAuth()
  const cache = useQueryClient()
  const [draft, setDraft] = useState("")
  const [runID, setRunID] = useState("")
  const transcript = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLTextAreaElement>(null)
  const follow = useRef(true)
  const run = useQuery({
    queryKey: ["run", runID],
    queryFn: ({ signal }) => getRun(runID, signal),
    enabled: !!runID,
    refetchInterval: (query) =>
      query.state.data?.turns.some((turn) => activeStatus(turn.status)) ? 1000 : false,
  })
  const working = run.data?.turns.some((turn) => activeStatus(turn.status)) ?? false
  const eventsKey = ["run-events", runID]
  const events = useQuery({
    queryKey: eventsKey,
    queryFn: ({ signal }) => loadEvents(
      runID, cache.getQueryData<RuntimeEvent[]>(eventsKey) ?? [], signal,
    ),
    enabled: !!runID,
    refetchInterval: working ? 1000 : false,
  })
  const live = liveOutputs(events.data ?? [], run.data)
  const refresh = async () => {
    await Promise.all([
      cache.invalidateQueries({ queryKey: ["run", runID] }),
      cache.invalidateQueries({ queryKey: eventsKey }),
    ])
  }
  const send = useMutation({
    mutationFn: async (question: string) => {
      follow.current = true
      const chat = await createDocumentChat(document.document_id)
      setRunID(chat.run_id)
      await appendMessage(chat.run_id, question)
      await Promise.all([
        cache.invalidateQueries({ queryKey: ["run", chat.run_id] }),
        cache.invalidateQueries({ queryKey: ["run-events", chat.run_id] }),
      ])
    },
    onSuccess: () => setDraft(""),
  })
  const stop = useMutation({
    mutationFn: () => interruptRun(runID),
    onSuccess: refresh,
  })
  const latestTurn = run.data?.turns.at(-1)
  const failure = documentQuestionFailure(latestTurn, events.data ?? [])
  const turnStatus = latestTurn?.status
  useEffect(() => {
    if (runID && turnStatus && !activeStatus(turnStatus)) {
      void cache.invalidateQueries({ queryKey: ["run-events", runID] })
    }
  }, [cache, runID, turnStatus])
  const messages = run.data?.messages ?? []
  const disabled = working || send.isPending ||
    (!!runID && !run.data)
  const liveText = live.at(-1)?.[1]
  useEffect(() => {
    if (!follow.current) return
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches
    transcript.current?.scrollTo({ top: transcript.current.scrollHeight, behavior: reducedMotion ? "instant" : "smooth" })
  }, [messages.length, liveText])
  useLayoutEffect(() => {
    const element = input.current
    if (!element) return
    let width = element.clientWidth
    const resize = () => {
      element.style.height = "auto"
      element.style.height = `${element.scrollHeight}px`
    }
    resize()
    const observer = new ResizeObserver(() => {
      if (element.clientWidth === width) return
      width = element.clientWidth
      resize()
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [draft])

  return (
    <section className="panel document-chat-panel">
      <header className="document-metadata-heading">
        <h2>Ask about this document</h2>
      </header>
      <p className="document-chat-description">Test our document extraction. Answers use only this document and don’t have access to other resources.</p>
      <div className="document-chat-messages" ref={transcript} onScroll={(event) => {
        const element = event.currentTarget
        follow.current = element.scrollHeight - element.scrollTop - element.clientHeight < 56
      }} role="log" aria-label="Document questions and answers" aria-live="polite">
        {messages.map((message) => (
          <ChatMessage key={message.message_id} role={message.role}
            content={message.display_content || message.content} userAvatarURL={auth.user?.avatar_url} />
        ))}
        {live.map(([id, content]) => <ChatMessage key={id} role="assistant" content={content} />)}
        {working && !live.length && (
          <p className="document-chat-working" role="status"><LoaderCircle className="spin" size={15} /> Searching this document…</p>
        )}
        {failure && (
          <div className="document-question-error" role="alert">
            <AlertCircle size={18} aria-hidden="true" />
            <div><strong>{failure.title}</strong><p>{failure.message}</p></div>
          </div>
        )}
      </div>
      {(run.error || events.error || send.error || stop.error) && (
        <p className="runtime-error" role="alert">
          {errorMessage(run.error || events.error || send.error || stop.error)}
          {run.error && (
            <button className="secondary-button" type="button"
              onClick={() => void run.refetch()}>Retry</button>
          )}
        </p>
      )}
      <form className="runtime-composer document-chat-composer" onSubmit={(event) => {
        event.preventDefault()
        if (!disabled && draft.trim()) send.mutate(draft.trim())
      }}>
        <textarea aria-label="Ask about this document" placeholder="Ask about this document…"
          ref={input} rows={1} value={draft} maxLength={16000} disabled={disabled}
          onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault()
              event.currentTarget.form?.requestSubmit()
            }
          }} />
        <div>
          {working ? (
            <button className="secondary-button" type="button" aria-label="Stop answering" title="Stop answering"
              disabled={stop.isPending} onClick={() => stop.mutate()}>
              {stop.isPending ? <LoaderCircle className="spin" size={14} /> : <Square size={12} />}
            </button>
          ) : (
            <button className="primary-button" type="submit" aria-label="Send document question" disabled={disabled || !draft.trim()}>
              {send.isPending ? <LoaderCircle className="spin" size={14} /> : <ArrowUp size={14} />}
            </button>
          )}
        </div>
      </form>
    </section>
  )
}
