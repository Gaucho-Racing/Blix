import { useMutation, useQueryClient } from "@tanstack/react-query"
import { LoaderCircle, Pencil } from "lucide-react"
import { useState } from "react"
import { Link } from "react-router-dom"
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
  const missing =
    extracted &&
    (!extracted.title ||
      !extracted.publisher ||
      !extracted.version ||
      !extracted.kind ||
      !extracted.published_at)
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
      <header className="content-heading">
        <div>
          <h2>Document details</h2>
          <span>
            {extracted
              ? "Identified by Blix. Review or correct any field."
              : active
                ? "Blix will identify these after reading the document."
                : "Document details have not been identified yet."}
          </span>
        </div>
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
      {missing && (
        <p className="document-help">
          Some details could not be established. Unknown fields are left blank
          for review.
        </p>
      )}
      {extracted?.warnings?.map((warning, index) => (
        <p className="document-help" key={index}>
          {warning}
        </p>
      ))}
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
              Corrections update this document’s details. Existing wiki drafts
              retain their original content; review them separately or ask Blix
              to revise them.
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
      {!!extracted?.source_record_ids?.length && (
        <div className="document-metadata-sources">
          <span>Identified from: </span>
          {extracted.source_record_ids.map((id) => {
            const page = details.pages.find(
              (page) => page.source_record_id === id,
            )
            return page ? (
              <Link
                key={id}
                to={`/knowledge/documents/${document.document_id}?page=${page.page_number}`}
              >
                PDF page {page.page_number}
              </Link>
            ) : null
          })}
        </div>
      )}
    </section>
  )
}
