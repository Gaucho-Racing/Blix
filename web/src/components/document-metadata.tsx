import { useMutation, useQueryClient } from "@tanstack/react-query"
import { LoaderCircle, Pencil, Plus, X } from "lucide-react"
import { useId, useState } from "react"
import { useAuth } from "@/lib/auth"
import { documentProcessing, updateDocumentMetadata } from "@/lib/documents"
import type { DocumentDetails, DocumentMetadataFields } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"
import { DocumentTags } from "@/components/document-tags"
import { addDocumentTag } from "@/lib/document-tags"

const labels: Record<Exclude<keyof DocumentMetadataFields, "tags">, string> = {
  title: "Document title",
  publisher: "Publisher",
  version: "Version / edition",
  published_at: "Publication / revision date",
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
    tags: document.tags,
    published_at: document.published_at?.slice(0, 10) ?? "",
    external_url: document.external_url,
  }
  const [fields, setFields] = useState(initial)
  const [tagDraft, setTagDraft] = useState("")
  const [tagError, setTagError] = useState("")
  const tagInputID = useId()
  const changedFields = (next: DocumentMetadataFields) =>
    Object.fromEntries(
      Object.entries(next).filter(
        ([key, value]) =>
          JSON.stringify(value) !==
          JSON.stringify(initial[key as keyof DocumentMetadataFields]),
      ),
    )
  const changed = changedFields(fields)
  const addTag = () => {
    try {
      setFields({ ...fields, tags: addDocumentTag(fields.tags, tagDraft) })
      setTagDraft("")
      setTagError("")
    } catch (error) {
      setTagError(errorMessage(error))
    }
  }
  const active =
    documentProcessing(document.status) || document.status === "uploading"
  const canEdit =
    auth.user?.entity_id === document.uploaded_by &&
    !active &&
    document.review_status !== "rejected"
  const extracted = document.extracted_metadata
  const save = useMutation({
    mutationFn: (patch: Partial<DocumentMetadataFields>) =>
      updateDocumentMetadata(
        document.document_id,
        document.metadata_revision,
        patch,
      ),
    onSuccess: (data) => {
      cache.setQueryData(["document", document.document_id], data)
      void cache.invalidateQueries({ queryKey: ["documents"] })
      setEditing(false)
    },
  })
  if (!extracted && active) return null

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
            try {
              const next = {
                ...fields,
                tags: addDocumentTag(fields.tags, tagDraft),
              }
              const patch = changedFields(next)
              setFields(next)
              setTagDraft("")
              setTagError("")
              if (Object.keys(patch).length) save.mutate(patch)
            } catch (error) {
              setTagError(errorMessage(error))
            }
          }}
        >
          <fieldset disabled={save.isPending}>
            <div className="document-form-grid">
              {(Object.keys(labels) as (keyof typeof labels)[]).map((key) => (
                <label key={key}>
                  {labels[key]}
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
                </label>
              ))}
            </div>
            <div className="document-tag-editor">
              <label htmlFor={tagInputID}>Tags</label>
              {!!fields.tags.length && (
                <div className="document-tags">
                  {fields.tags.map((tag) => (
                    <span className="document-tag" key={tag}>
                      {tag}
                      <button
                        type="button"
                        aria-label={`Remove tag ${tag}`}
                        onClick={() => {
                          setFields({
                            ...fields,
                            tags: fields.tags.filter((value) => value !== tag),
                          })
                          setTagError("")
                        }}
                      >
                        <X size={12} />
                      </button>
                    </span>
                  ))}
                </div>
              )}
              <div className="document-tag-add">
                <input
                  id={tagInputID}
                  value={tagDraft}
                  placeholder="Add a tag…"
                  maxLength={64}
                  aria-invalid={!!tagError}
                  aria-describedby={
                    tagError ? `${tagInputID}-error` : undefined
                  }
                  onChange={(event) => {
                    setTagDraft(event.target.value)
                    setTagError("")
                  }}
                  onKeyDown={(event) => {
                    if (
                      event.key === "Enter" &&
                      !event.nativeEvent.isComposing
                    ) {
                      event.preventDefault()
                      addTag()
                    }
                  }}
                />
                <button
                  className="secondary-button"
                  type="button"
                  disabled={!tagDraft.trim()}
                  onClick={addTag}
                >
                  <Plus size={14} /> Add
                </button>
              </div>
              {tagError && (
                <p
                  className="runtime-error"
                  role="alert"
                  id={`${tagInputID}-error`}
                >
                  {tagError}
                </p>
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
                disabled={!Object.keys(changed).length && !tagDraft.trim()}
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
                  setTagDraft("")
                  setTagError("")
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
          {(Object.keys(labels) as (keyof typeof labels)[]).map((key) => (
            <div key={key}>
              <dt>
                {labels[key]}{" "}
                {document.metadata_overrides?.[key] && <span>Edited</span>}
              </dt>
              <dd>{initial[key] || "Not established"}</dd>
            </div>
          ))}
          <div className="document-metadata-tags">
            <dt>
              Tags {document.metadata_overrides?.tags && <span>Edited</span>}
            </dt>
            <dd>
              {initial.tags.length ? (
                <DocumentTags tags={initial.tags} />
              ) : (
                "No tags"
              )}
            </dd>
          </div>
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
