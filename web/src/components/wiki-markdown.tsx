import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import { Link } from "react-router-dom"
import { wikiLink } from "@/lib/wiki-links"

export function WikiMarkdown({
  content,
  path,
  proposedPaths = [],
  onSelectPath,
}: {
  content: string
  path: string
  proposedPaths?: string[]
  onSelectPath?: (path: string) => void
}) {
  return (
    <div className="wiki-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        components={{
          a: ({ href, children }) => {
            if (!href) return <span>{children}</span>
            const link = wikiLink(href, path, proposedPaths)
            const target = /^#file-\d+$/.test(link.href)
              ? proposedPaths[Number(link.href.slice(6))]
              : undefined
            if (onSelectPath && target) {
              return (
                <a
                  href={link.href}
                  onClick={(event) => {
                    event.preventDefault()
                    onSelectPath(target)
                  }}
                >
                  {children}
                </a>
              )
            }
            return link.external ? (
              <a href={link.href} target="_blank" rel="noreferrer">
                {children}
              </a>
            ) : link.href.startsWith("#") ? (
              <a href={link.href}>{children}</a>
            ) : (
              <Link to={link.href}>{children}</Link>
            )
          },
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}
