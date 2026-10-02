import { LoaderCircle } from "lucide-react"
import { useEffect, useRef, type FormEvent, type ReactNode } from "react"

export function ReviewConfirmationDialog({
  title,
  description,
  confirmLabel,
  reject,
  kicker = "KNOWLEDGE REVIEW",
  note,
  setNote,
  pending,
  allowed,
  error,
  onClose,
  onSubmit,
}: {
  title: string
  description: ReactNode
  confirmLabel: string
  reject: boolean
  kicker?: string
  note: string
  setNote: (value: string) => void
  pending: boolean
  allowed: boolean
  error: string | null
  onClose: () => void
  onSubmit: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = ref.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [])
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!pending && allowed) onSubmit()
  }
  return (
    <dialog
      aria-labelledby="review-dialog-title"
      className="knowledge-review-dialog"
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
      ref={ref}
    >
      <form onSubmit={submit}>
        <div className="eyebrow">{kicker}</div>
        <h2 id="review-dialog-title">{title}</h2>
        <div className="review-confirmation-description">{description}</div>
        <label htmlFor="review-note">
          {reject ? "Reason for rejection" : "Review note (optional)"}
        </label>
        <textarea
          disabled={pending}
          id="review-note"
          maxLength={2000}
          onChange={(event) => setNote(event.target.value)}
          placeholder={
            reject
              ? "Add a note to the review record."
              : "Anything future reviewers should know?"
          }
          required={reject}
          rows={4}
          value={note}
        />
        {error && (
          <div className="error-state" role="alert">
            {error}
          </div>
        )}
        <footer>
          <button
            className="secondary-button"
            disabled={pending}
            onClick={onClose}
            type="button"
          >
            Cancel
          </button>
          <button
            className={`primary-button${reject ? " knowledge-reject-confirm" : ""}`}
            disabled={pending || !allowed || (reject && !note.trim())}
            type="submit"
          >
            {pending && <LoaderCircle className="spin" size={15} />}
            {pending ? "Saving decision…" : confirmLabel}
          </button>
        </footer>
      </form>
    </dialog>
  )
}
