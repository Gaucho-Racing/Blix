import { useQuery } from "@tanstack/react-query"
import { FileText, X } from "lucide-react"
import { Link } from "react-router-dom"
import { ThemedSelect } from "@/components/themed-select"
import { documentReviewLabel, listDocuments } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

export function ChatDocumentContext({
  documentIDs,
  onChange,
  disabled,
}: {
  documentIDs: string[]
  onChange?: (ids: string[]) => void
  disabled: boolean
}) {
  const documents = useQuery({
    queryKey: ["documents"],
    queryFn: listDocuments,
    enabled: !disabled,
    refetchInterval: 5000,
  })
  const available =
    documents.data?.filter(
      (document) =>
        document.size_bytes > 0 &&
        document.review_status !== "rejected" &&
        !documentIDs.includes(document.document_id),
    ) ?? []
  if (disabled || (!onChange && !documentIDs.length)) return null
  return (
    <section
      className="chat-document-context"
      aria-label="Conversation documents"
    >
      {documentIDs.length > 0 && (
        <ul>
          {documentIDs.map((id) => {
            const document = documents.data?.find(
              (item) => item.document_id === id,
            )
            return (
              <li key={id}>
                <FileText size={15} />
                <Link to={`/knowledge/documents/${id}`}>
                  {document?.title || "Attached document"}
                </Link>
                {document && (
                  <span>{documentReviewLabel(document.review_status)}</span>
                )}
                {onChange && (
                  <button
                    type="button"
                    aria-label={`Remove ${document?.title || "document"}`}
                    onClick={() =>
                      onChange(documentIDs.filter((value) => value !== id))
                    }
                  >
                    <X size={14} />
                  </button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {onChange && documentIDs.length < 8 && (
        <ThemedSelect
          label="Attach a document to this conversation"
          value="choose"
          onValueChange={(id) => {
            if (
              id !== "choose" &&
              available.some((document) => document.document_id === id)
            )
              onChange([...documentIDs, id])
          }}
          options={[
            {
              value: "choose",
              label: documents.isPending
                ? "Loading documents…"
                : "Attach a document…",
            },
            ...available.map((document) => ({
              value: document.document_id,
              label: `${document.title} · ${documentReviewLabel(document.review_status)}`,
            })),
          ]}
        />
      )}
      {documents.error && (
        <p className="runtime-error" role="alert">
          {errorMessage(documents.error)}{" "}
          <button type="button" onClick={() => void documents.refetch()}>
            Retry
          </button>
        </p>
      )}
    </section>
  )
}
