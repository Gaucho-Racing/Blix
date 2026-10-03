import {
  Circle,
  CircleCheck,
  CircleMinus,
  CircleX,
  LoaderCircle,
} from "lucide-react"
import {
  documentEmbeddingStep,
  documentIngestionSteps,
  type DocumentStage,
  type DocumentDetails,
} from "@/lib/documents"

const statusLabels: Record<DocumentStage["status"], string> = {
  waiting: "Waiting",
  running: "In progress",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Stopped",
}

export function DocumentIngestionTimeline({
  details,
}: {
  details: DocumentDetails
}) {
  const { document } = details
  const embedding = details.search_index ? documentEmbeddingStep(details.search_index) : null
  const finished = ["ready", "partial", "no_changes"].includes(document.status)
  const steps: {
    name: string
    label: string
    detail: string
    status: DocumentStage["status"]
  }[] = documentIngestionSteps.map(({ name, label }) => ({
    name,
    label,
    detail: "",
    status: document.stages?.find((item) => item.name === name)?.status ??
      (finished ? "completed" : "waiting"),
  }))
  if (embedding) steps.push({ name: "embedding", label: "Generate embeddings", ...embedding })
  return (
    <ol className="ingestion-timeline" aria-label="Document processing steps">
      {steps.map(({ name, label, status, detail }) => {
        return (
          <li
            key={name}
            className={`timeline-step-${status}`}
            aria-current={status === "running" && document.status === name ? "step" : undefined}
            aria-busy={status === "running" || undefined}
          >
            <span className="timeline-icon" aria-hidden="true">
              <span className="timeline-glyph timeline-waiting">
                <Circle size={18} />
              </span>
              <span className="timeline-glyph timeline-running">
                <LoaderCircle size={18} className="spin" />
              </span>
              <span className="timeline-glyph timeline-completed">
                <CircleCheck size={18} />
              </span>
              <span className="timeline-glyph timeline-failed">
                <CircleX size={18} />
              </span>
              <span className="timeline-glyph timeline-cancelled">
                <CircleMinus size={18} />
              </span>
            </span>
            <span className="timeline-label">
              {label}
              {detail && <span className="timeline-step-detail">{detail}</span>}
              <span className="timeline-status-text">
                : {statusLabels[status]}
              </span>
            </span>
          </li>
        )
      })}
    </ol>
  )
}
