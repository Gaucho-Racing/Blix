import {
  Circle,
  CircleCheck,
  CircleMinus,
  CircleX,
  LoaderCircle,
} from "lucide-react"
import type { DocumentStage, TeamDocument } from "@/lib/documents"

const steps: { name: DocumentStage["name"]; label: string }[] = [
  { name: "uploading", label: "Upload document" },
  { name: "extracting", label: "Extract document" },
  { name: "indexing", label: "Make searchable" },
  { name: "analyzing_visuals", label: "Extract visual evidence" },
  { name: "identifying_metadata", label: "Identify document details" },
  { name: "reading", label: "Read document" },
  { name: "reconciling", label: "Draft wiki updates" },
]

const statusLabels: Record<DocumentStage["status"], string> = {
  waiting: "Waiting",
  running: "In progress",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Stopped",
}

export function DocumentIngestionTimeline({
  document,
}: {
  document: TeamDocument
}) {
  const finished = ["ready", "partial", "no_changes"].includes(document.status)
  return (
    <ol className="ingestion-timeline" aria-label="Document processing steps">
      {steps.map(({ name, label }) => {
        const stage = document.stages?.find((item) => item.name === name)
        const status = stage?.status ?? (finished ? "completed" : "waiting")
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
