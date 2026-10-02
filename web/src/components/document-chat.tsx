import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowUp, LoaderCircle, Square } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { ChatMessage } from "@/components/chat-message"
import { useAuth } from "@/lib/auth"
import { createDocumentChat, getDocumentChat } from "@/lib/document-chat"
import type { DocumentDetails } from "@/lib/documents"
import {
  activeStatus, appendMessage, errorMessage, getConversation,
  interruptConversation, liveOutputs, loadEvents,
} from "@/lib/runtime"
import type { RuntimeEvent } from "@/lib/runtime"

export function DocumentChat({ details }: { details: DocumentDetails }) {
  const { document } = details
  const auth = useAuth()
  const cache = useQueryClient()
  const [draft, setDraft] = useState("")
  const transcript = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const lookupKey = ["document-chat", document.document_id, auth.user?.entity_id]
  const lookup = useQuery({
    queryKey: lookupKey,
    queryFn: ({ signal }) => getDocumentChat(document.document_id, signal),
  })
  const conversationID = lookup.data?.conversation_id ?? ""
  const conversation = useQuery({
    queryKey: ["conversation", conversationID],
    queryFn: ({ signal }) => getConversation(conversationID, signal),
    enabled: !!conversationID,
    refetchInterval: (query) =>
      query.state.data?.turns.some((turn) => activeStatus(turn.status)) ? 1000 : false,
  })
  const working = conversation.data?.turns.some((turn) => activeStatus(turn.status)) ?? false
  const eventsKey = ["conversation-events", conversationID]
  const events = useQuery({
    queryKey: eventsKey,
    queryFn: ({ signal }) => loadEvents(
      conversationID, cache.getQueryData<RuntimeEvent[]>(eventsKey) ?? [], signal,
    ),
    enabled: !!conversationID,
    refetchInterval: working ? 1000 : false,
  })
  const live = liveOutputs(events.data ?? [], conversation.data)
  const refresh = async () => {
    await Promise.all([
      cache.invalidateQueries({ queryKey: ["conversation", conversationID] }),
      cache.invalidateQueries({ queryKey: eventsKey }),
    ])
  }
  const send = useMutation({
    mutationFn: async (question: string) => {
      follow.current = true
      const chat = lookup.data ?? await createDocumentChat(document.document_id)
      cache.setQueryData(lookupKey, chat)
      await appendMessage(chat.conversation_id, question)
      await Promise.all([
        cache.invalidateQueries({ queryKey: ["conversation", chat.conversation_id] }),
        cache.invalidateQueries({ queryKey: ["conversation-events", chat.conversation_id] }),
      ])
    },
    onSuccess: () => setDraft(""),
  })
  const stop = useMutation({
    mutationFn: () => interruptConversation(conversationID),
    onSuccess: refresh,
  })
  const latestTurn = conversation.data?.turns.at(-1)
  const messages = conversation.data?.messages ?? []
  const disabled = working || send.isPending || lookup.isPending || lookup.isError ||
    (!!conversationID && !conversation.data)
  const liveText = live.at(-1)?.[1]
  useEffect(() => {
    if (!follow.current) return
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches
    transcript.current?.scrollTo({ top: transcript.current.scrollHeight, behavior: reducedMotion ? "instant" : "smooth" })
  }, [messages.length, liveText])

  return (
    <section className="panel document-chat-panel">
      <header className="document-metadata-heading">
        <h2>Ask about this document</h2>
      </header>
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
        {!messages.length && !lookup.isPending && (
          <p className="document-chat-empty">Test our document extraction. Answers use only this document and don’t have access to other resources.</p>
        )}
        {latestTurn && ["failed", "expired", "cancelled"].includes(latestTurn.status) && (
          <p className="runtime-error" role="status">
            {latestTurn.status === "cancelled" ? "Answer stopped." : "Blix couldn’t finish answering. You can send the question again."}
          </p>
        )}
      </div>
      {(lookup.error || conversation.error || events.error || send.error || stop.error) && (
        <p className="runtime-error" role="alert">
          {errorMessage(lookup.error || conversation.error || events.error || send.error || stop.error)}
          {(lookup.error || conversation.error) && (
            <button className="secondary-button" type="button"
              onClick={() => void (lookup.error ? lookup.refetch() : conversation.refetch())}>Retry</button>
          )}
        </p>
      )}
      <form className="runtime-composer document-chat-composer" onSubmit={(event) => {
        event.preventDefault()
        if (!disabled && draft.trim()) send.mutate(draft.trim())
      }}>
        <textarea aria-label="Ask about this document" placeholder="Ask a question about this document…"
          rows={2} value={draft} maxLength={16000} disabled={disabled}
          onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault()
              event.currentTarget.form?.requestSubmit()
            }
          }} />
        <div>
          <span>Enter to send · Shift + Enter for a new line</span>
          {working && <button className="secondary-button" type="button" disabled={stop.isPending}
            onClick={() => stop.mutate()}><Square size={13} /> Stop</button>}
          <button className="primary-button" type="submit" aria-label="Send document question" disabled={disabled || !draft.trim()}>
            {send.isPending ? <LoaderCircle className="spin" size={16} /> : <ArrowUp size={16} />}
          </button>
        </div>
      </form>
    </section>
  )
}
