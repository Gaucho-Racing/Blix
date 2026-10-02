import { useQuery } from "@tanstack/react-query"
import { Search } from "lucide-react"
import { useState } from "react"
import { Link } from "react-router-dom"
import { listAdminConversations, errorMessage } from "@/lib/runtime"
import { formatTimestamp } from "@/lib/format"

export function RunsPage() {
  const [search, setSearch] = useState("")
  const query = useQuery({
    queryKey: ["admin", "conversations"],
    queryFn: listAdminConversations,
    refetchInterval: 5000,
  })
  const filtered = query.data?.filter((conversation) =>
    `${conversation.name} ${conversation.conversation_id}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  )
  return (
    <div className="page">
      <header className="page-header">
        <div>
          <div className="eyebrow">ADMIN · RUNTIME</div>
          <h1>Conversations</h1>
          <p>
            Inspect durable conversations, turns, delegated tasks, and activity.
          </p>
        </div>
      </header>
      <section className="panel">
        <div className="panel-toolbar">
          <h2>Recent conversations</h2>
          <label className="search-box">
            <Search size={15} />
            <input
              aria-label="Search conversations"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Search name or ID"
            />
          </label>
        </div>
        {query.isPending && <p className="loading-state">Loading…</p>}
        {query.error && (
          <p className="runtime-error" role="alert">
            {errorMessage(query.error)}
          </p>
        )}
        {filtered?.length === 0 && (
          <p className="empty-state">No matching conversations.</p>
        )}
        <div className="run-table-wrap">
          <table className="run-table">
            <thead>
              <tr>
                <th>Conversation</th>
                <th>Status</th>
                <th>Last activity</th>
              </tr>
            </thead>
            <tbody>
              {filtered?.map((conversation) => (
                <tr key={conversation.conversation_id}>
                  <td>
                    <Link
                      className="run-link"
                      to={`/admin/runs/${conversation.conversation_id}`}
                    >
                      <strong>{conversation.name || "New conversation"}</strong>
                      <code>{conversation.conversation_id}</code>
                    </Link>
                  </td>
                  <td>{conversation.status}</td>
                  <td>{formatTimestamp(conversation.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  )
}
