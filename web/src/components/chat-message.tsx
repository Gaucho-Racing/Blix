import Markdown from "react-markdown"
import remarkGfm from "remark-gfm"

interface ChatMessageProps {
  role: "user" | "assistant"
  content: string
  userAvatarURL?: string
}

export function ChatMessage({ role, content, userAvatarURL }: ChatMessageProps) {
  if (role === "user") {
    return (
      <article className="chat-message chat-message-user">
        <div className="chat-message-user-copy">{content}</div>
        {userAvatarURL ? <img alt="" className="chat-message-avatar" src={userAvatarURL} /> : <span className="chat-message-avatar chat-message-avatar-fallback">You</span>}
      </article>
    )
  }

  return (
    <article className="chat-message chat-message-assistant">
      <span className="chat-message-blix-avatar">B</span>
      <div className="chat-message-body">
        <span className="chat-message-author">Blix</span>
        <Markdown
          components={{
            a: ({ children, ...properties }) => <a {...properties} rel="noreferrer" target="_blank">{children}</a>,
          }}
          remarkPlugins={[remarkGfm]}
        >
          {content}
        </Markdown>
      </div>
    </article>
  )
}
