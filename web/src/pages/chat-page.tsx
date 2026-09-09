import { ArrowUp, BookOpen, Clock3, FileSearch, Hash, Paperclip, Plus, Sparkles } from "lucide-react"
import { useState } from "react"
import type { FormEvent } from "react"

const prompts = [
  {
    icon: BookOpen,
    label: "Find a decision",
    prompt: "What did we decide about accumulator segment packaging?",
  },
  {
    icon: FileSearch,
    label: "Research",
    prompt: "Research the biggest Formula SAE EV rule changes for 2026.",
  },
  {
    icon: Clock3,
    label: "Coordinate",
    prompt: "Draft a reminder for owners of overdue electrical tasks.",
  },
]

const recentThreads = [
  { meta: "Deep research · 18 min", title: "2026 Formula SAE EV results" },
  { meta: "Wiki + Discord · Yesterday", title: "Accumulator inspection checklist" },
  { meta: "4 sources · Sep 6", title: "Brake light CAN debugging" },
]

export function ChatPage() {
  const [draft, setDraft] = useState("")
  const [submittedPrompt, setSubmittedPrompt] = useState("")

  function submitPreview(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const message = draft.trim()
    if (!message) return
    setSubmittedPrompt(message)
    setDraft("")
  }

  return (
    <div className="workspace-page chat-page">
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">TEAM WORKSPACE</span>
          <h1>Chat</h1>
        </div>
        <button className="secondary-button" type="button"><Plus size={16} /> New conversation</button>
      </header>

      <div className="chat-layout">
        <main className="chat-stage">
          <section className="chat-welcome">
            <div className="blix-spark"><Sparkles size={23} /></div>
            <span>BLIX</span>
            <h2>What should we work on?</h2>
            <p>Ask about team knowledge, research a problem, or kick off work across Gaucho Racing.</p>
            <div className="source-strip" aria-label="Available knowledge sources">
              <span><i /> Wiki ready</span>
              <span><i /> Discord synced</span>
              <span><i /> Tools available</span>
            </div>
          </section>

          {submittedPrompt && (
            <section className="preview-conversation" aria-live="polite">
              <div className="preview-message preview-user"><span>You</span><p>{submittedPrompt}</p></div>
              <div className="preview-message preview-blix">
                <span>Blix</span>
                <p>This is where the live Runway response will stream into the conversation.</p>
              </div>
            </section>
          )}

          <form className="prompt-composer" onSubmit={submitPreview}>
            <textarea
              aria-label="Message Blix"
              onChange={(event) => setDraft(event.currentTarget.value)}
              placeholder="Ask Blix anything…"
              rows={3}
              value={draft}
            />
            <div className="composer-footer">
              <div>
                <button aria-label="Attach context" className="icon-button" type="button"><Paperclip size={17} /></button>
                <span className="agent-mode"><Sparkles size={14} /> Agent mode</span>
              </div>
              <button aria-label="Send message" className="send-button" disabled={!draft.trim()} type="submit"><ArrowUp size={18} /></button>
            </div>
          </form>

          {!submittedPrompt && (
            <section className="prompt-grid" aria-label="Suggested prompts">
              {prompts.map(({ icon: Icon, label, prompt }) => (
                <button className="prompt-card" key={label} onClick={() => setDraft(prompt)} type="button">
                  <Icon size={17} />
                  <span>{label}</span>
                  <p>{prompt}</p>
                </button>
              ))}
            </section>
          )}
        </main>

        <aside className="recent-work-panel">
          <div className="recent-work-heading">
            <div><Hash size={15} /><h2>Recent work</h2></div>
            <button aria-label="View all recent work" type="button">View all</button>
          </div>
          <div className="recent-thread-list">
            {recentThreads.map((thread) => (
              <button className="recent-thread" key={thread.title} type="button">
                <span>{thread.title}</span>
                <small>{thread.meta}</small>
              </button>
            ))}
          </div>
          <div className="recent-work-footer"><span className="healthy-dot" /> Knowledge sources are up to date</div>
        </aside>
      </div>
    </div>
  )
}
