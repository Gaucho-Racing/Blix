import { useMutation, useQueryClient } from "@tanstack/react-query"
import { LoaderCircle, Pencil } from "lucide-react"
import { useState } from "react"
import { useAuth } from "@/lib/auth"
import { documentProcessing, updateDocumentMetadata } from "@/lib/documents"
import type { DocumentDetails, DocumentMetadataFields } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

const labels: Record<keyof DocumentMetadataFields, string> = {
  title: "Document title",
  publisher: "Publisher",
  version: "Version / edition",
  published_at: "Publication / revision date",
  kind: "Document type",
  external_url: "Official source URL",
}

export function DocumentMetadata({ details }: { details: DocumentDetails }) {
  const { document } = details
  const auth = useAuth()
  const cache = useQueryClient()
  const [editing, setEditing] = useState(false)
  const initial: DocumentMetadataFields = {
    title: document.title,
    publisher: document.publisher,
    version: document.version,
    kind: document.kind,
    published_at: document.published_at?.slice(0, 10) ?? "",
    external_url: document.external_url,
  }
  const [fields, setFields] = useState(initial)
  const changed = Object.entries(fields).filter(
    ([key, value]) => value !== initial[key as keyof DocumentMetadataFields],
  )
  const active =
    documentProcessing(document.status) || document.status === "uploading"
  const canEdit = auth.user?.entity_id === document.uploaded_by && !active
  const extracted = document.extracted_metadata
  const save = useMutation({
    mutationFn: () =>
      updateDocumentMetadata(
        document.document_id,
        document.metadata_revision,
        Object.fromEntries(changed),
      ),
    onSuccess: (data) => {
      cache.setQueryData(["document", document.document_id], data)
      void cache.invalidateQueries({ queryKey: ["documents"] })
      setEditing(false)
    },
  })
  return (
    <section className="panel document-metadata-panel">
      <header className="document-metadata-heading">
        <h2>Document details</h2>
        {canEdit && !editing && (
          <button
            className="secondary-button"
            type="button"
            onClick={() => setEditing(true)}
          >
            <Pencil size={14} /> Edit details
          </button>
        )}
      </header>
      {editing ? (
        <form
          className="document-form"
          onSubmit={(event) => {
            event.preventDefault()
            if (changed.length) save.mutate()
          }}
        >
          <fieldset disabled={save.isPending}>
            <div className="document-form-grid">
              {(Object.keys(labels) as (keyof DocumentMetadataFields)[]).map(
                (key) => (
                  <label key={key}>
                    {labels[key]}
                    {key === "kind" ? (
                      <select
                        value={fields.kind}
                        onChange={(event) =>
                          setFields({
                            ...fields,
                            kind: event.target
                              .value as DocumentMetadataFields["kind"],
                          })
                        }
                      >
                        <option value="rules">Rules / requirements</option>
                        <option value="roadmap">
                          Roadmap / future guidance
                        </option>
                        <option value="reference">Reference material</option>
                      </select>
                    ) : (
                      <input
                        value={fields[key]}
                        required={key === "title"}
                        maxLength={key === "external_url" ? 4096 : 512}
                        type={
                          key === "published_at"
                            ? "date"
                            : key === "external_url"
                              ? "url"
                              : "text"
                        }
                        placeholder="Not established"
                        onChange={(event) =>
                          setFields({ ...fields, [key]: event.target.value })
                        }
                      />
                    )}
                  </label>
                ),
              )}
            </div>
            <p className="document-help">
              These edits update document details. To revise a wiki draft,
              discuss it with Blix.
            </p>
            {save.error && (
              <p className="runtime-error" role="alert">
                {errorMessage(save.error)}
              </p>
            )}
            <div className="ingestion-actions">
              <button
                className="primary-button"
                disabled={!changed.length}
                type="submit"
              >
                {save.isPending && <LoaderCircle className="spin" size={14} />}{" "}
                Save details
              </button>
              <button
                className="secondary-button"
                type="button"
                onClick={() => {
                  setEditing(false)
                  setFields(initial)
                  save.reset()
                }}
              >
                Cancel
              </button>
            </div>
          </fieldset>
        </form>
      ) : extracted || !active ? (
        <dl className="document-metadata-grid">
          {(Object.keys(labels) as (keyof DocumentMetadataFields)[]).map(
            (key) => (
              <div key={key}>
                <dt>
                  {labels[key]}{" "}
                  {document.metadata_overrides?.[key] && <span>Edited</span>}
                </dt>
                <dd>{initial[key] || "Not established"}</dd>
              </div>
            ),
          )}
        </dl>
      ) : null}
      {!!extracted?.warnings?.length && (
        <details className="document-metadata-caveats">
          <summary>
            Notes on document details · {extracted.warnings.length}
          </summary>
          <ul>
            {extracted.warnings.map((warning, index) => (
              <li key={index}>{warning}</li>
            ))}
          </ul>
        </details>
      )}
    </section>
  )
}
