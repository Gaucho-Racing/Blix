import { DocumentPageImage } from "@/components/document-page-image"
import Markdown from "react-markdown"
import remarkGfm from "remark-gfm"

const depotPublicImage = /^https:\/\/depot\.gauchoracing\.com\/api\/download\/file_[0-9A-Za-z]{26}$/

interface ChatMessageProps {
  role: "user" | "assistant"
  content: string
  userAvatarURL?: string
  messageID?: string
}

export function ChatMessage({ role, content, userAvatarURL, messageID }: ChatMessageProps) {
  if (role === "user") {
    return (
      <article className="chat-message chat-message-user">
        <div className="chat-message-user-copy">{content}</div>
        {userAvatarURL ? <img alt="" className="chat-message-avatar" src={userAvatarURL} /> : <span className="chat-message-avatar chat-message-avatar-fallback">You</span>}
      </article>
    )
  }

  return (
    <article className="chat-message chat-message-assistant" id={messageID ? `message-${messageID}` : undefined}>
      <img className="chat-message-blix-avatar" src="/blix-logo.png" alt="" width={31} height={31} />
      <div className="chat-message-body">
        <span className="chat-message-author">Blix</span>
        <Markdown
          components={{
            img: ({ src, alt }) => typeof src === "string" && depotPublicImage.test(src)
              ? <a className="chat-message-image" href={src} target="_blank" rel="noreferrer"><img src={src} alt={alt || "Chart"} loading="lazy" /></a>
              : <DocumentPageImage src={src} alt={alt} />,
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
