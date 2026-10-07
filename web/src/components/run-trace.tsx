import { useMemo, useState } from "react"
import {
  Activity,
  ArrowDown,
  ArrowRight,
  Bot,
  Check,
  ChevronDown,
  CircleAlert,
  Clock3,
  Search,
  Terminal,
  UserRound,
  X,
} from "lucide-react"
import { buildTrace, traceTone } from "@/lib/run-trace"
import type { TraceActor, TraceEntry, TraceTone } from "@/lib/run-trace"
import type { RunDetails, RuntimeEvent } from "@/lib/runtime"
import { formatDuration, formatTimestamp } from "@/lib/format"

function Actor({ actor }: { actor: TraceActor }) {
  const Icon =
    actor.kind === "user"
      ? UserRound
      : actor.kind === "tool"
        ? Terminal
        : actor.kind === "system"
          ? Activity
          : Bot
  return (
    <span className={`trace-actor trace-actor-${actor.kind}`}>
      <Icon size={13} />
      <span>{actor.name}</span>
      {actor.instance && <small>#{actor.instance}</small>}
    </span>
  )
}
function Status({ text, tone }: { text: string; tone: TraceTone }) {
  const Icon =
    tone === "error"
      ? CircleAlert
      : tone === "success"
        ? Check
        : tone === "waiting"
          ? Clock3
          : Activity
  return (
    <span className={`trace-status trace-tone-${tone}`}>
      <Icon size={12} />
      {/^[a-z_]+$/.test(text) ? text.replaceAll("_", " ") : text}
    </span>
  )
}
function TraceCard({ entry, start }: { entry: TraceEntry; start?: string }) {
  const [rawOpen, setRawOpen] = useState(false)
  const { event } = entry
  const timestamp =
    event.created_at ??
    (typeof event.payload.timestamp === "string"
      ? event.payload.timestamp
      : undefined)
  const elapsed =
    timestamp && start
      ? new Date(timestamp).getTime() - new Date(start).getTime()
      : undefined
  const isLong = entry.content.length > 1000
  return (
    <article
      className={`trace-card trace-tone-${entry.tone} trace-category-${entry.category}`}
    >
      <div className="trace-card-heading">
        <div className="trace-route">
          <Actor actor={entry.from} />
          {entry.to && (
            <>
              <ArrowRight size={14} className="trace-route-arrow" />
              <Actor actor={entry.to} />
            </>
          )}
        </div>
        <span
          className="trace-event-time"
          title={
            timestamp ? formatTimestamp(timestamp) : "Timestamp unavailable"
          }
        >
          {elapsed !== undefined && Number.isFinite(elapsed) && (
            <span>+{formatDuration(elapsed)}</span>
          )}
          {timestamp && (
            <time dateTime={timestamp}>
              {new Date(timestamp).toLocaleTimeString([], {
                hour: "2-digit",
                minute: "2-digit",
                second: "2-digit",
              })}
            </time>
          )}
          <span>#{event.position}</span>
        </span>
      </div>
      <div className="trace-card-title">
        <h3>{entry.title}</h3>
        {entry.status && <Status tone={entry.tone} text={entry.status} />}
      </div>
      {entry.content && (
        <div
          className={`trace-card-content ${entry.category === "tool" ? "trace-tool-output" : ""}`}
        >
          {isLong ? (
            <details className="trace-long-content">
              <summary>
                <span>{entry.content.slice(0, 1000)}…</span>
                <span className="trace-expand">
                  Read full content <ChevronDown size={12} />
                </span>
              </summary>
              <div>{entry.content}</div>
            </details>
          ) : (
            entry.content
          )}
        </div>
      )}
      {event.payload.truncated === true && (
        <p className="trace-truncation">
          Output excerpt · the sandbox trace limit was reached.
        </p>
      )}
      <details
        className="trace-raw"
        onToggle={(e) => setRawOpen(e.currentTarget.open)}
      >
        <summary>
          <ChevronDown size={12} />
          Event data <code>{event.type}</code>
        </summary>
        <div className="trace-identifiers">
          <span>
            Session <code>{event.session_id}</code>
          </span>
          {event.task_id && (
            <span>
              Task <code>{event.task_id}</code>
            </span>
          )}
          {event.turn_id && (
            <span>
              Turn <code>{event.turn_id}</code>
            </span>
          )}
        </div>
        {rawOpen && <pre>{JSON.stringify(event.payload, null, 2)}</pre>}
      </details>
    </article>
  )
}

export function RunTrace({
  detail,
  events,
}: {
  detail?: RunDetails
  events: RuntimeEvent[]
}) {
  const entries = useMemo(() => buildTrace(events, detail), [events, detail])
  const [scope, setScope] = useState("all")
  const [mode, setMode] = useState("story")
  const [query, setQuery] = useState("")
  const [limit, setLimit] = useState(100)
  const taskActors = new Map<string, TraceActor>()
  for (const entry of entries) {
    for (const actor of [entry.from, entry.to])
      if (actor?.kind === "agent") taskActors.set(actor.id, actor)
  }
  const delegators = new Map(entries
    .filter(entry => entry.event.type === "task.created_v1" && entry.to)
    .map(entry => [entry.to!.id, entry.from.name]))
  const normalized = query.trim().toLowerCase()
  const filtered = entries.filter((entry) => {
    if (
      scope === "lead" &&
      entry.from.kind !== "lead" &&
      entry.to?.kind !== "lead"
    )
      return false
    if (scope !== "all" && scope !== "lead" && !entry.taskIDs.includes(scope))
      return false
    if (mode === "story" && entry.routine) return false
    if (
      mode === "conversation" &&
      !["conversation", "handoff"].includes(entry.category)
    )
      return false
    if (mode === "errors" && entry.tone !== "error" && entry.tone !== "waiting")
      return false
    return (
      !normalized ||
      `${entry.title} ${entry.content} ${entry.from.name} ${entry.to?.name ?? ""} ${entry.event.type} ${entry.taskIDs.join(" ")}`
        .toLowerCase()
        .includes(normalized)
    )
  })
  const shown = filtered.slice(0, limit)
  const turnNumbers = new Map(
    (detail?.turns ?? []).map((turn, index) => [turn.turn_id, index + 1]),
  )
  const selectedTask = detail?.tasks.find((task) => task.task_id === scope)
  const attention =
    detail?.tasks.filter((task) =>
      ["error", "waiting"].includes(traceTone(task.status)),
    ) ?? []
  const changeFilter = (update: () => void) => {
    update()
    setLimit(100)
  }
  return (
    <div className="run-trace-layout">
      <aside className="trace-sidebar">
        <section className="trace-roster panel">
          <div className="trace-section-heading">
            <span className="eyebrow">PARTICIPANTS</span>
            <h2>Who’s involved</h2>
          </div>
          <button
            className={`trace-participant ${scope === "all" ? "selected" : ""}`}
            onClick={() => changeFilter(() => setScope("all"))}
          >
            <span className="trace-participant-icon">
              <Activity size={17} />
            </span>
            <span>
              <strong>Entire run</strong>
              <small>
                {events.length.toLocaleString()} events ·{" "}
                {detail?.turns.length ?? 0} user turns
              </small>
            </span>
          </button>
          <button
            className={`trace-participant ${scope === "lead" ? "selected" : ""}`}
            onClick={() => changeFilter(() => setScope("lead"))}
          >
            <span className="trace-participant-icon trace-actor-lead">
              <Bot size={17} />
            </span>
            <span>
              <strong>Blix lead</strong>
              <small>Coordinates work · talks to the user</small>
            </span>
          </button>
          <div className="trace-delegation-line">
            <ArrowDown size={12} /> delegated work
          </div>
          {(detail?.tasks ?? []).map((task, index) => (
            <button
              key={task.task_id}
              className={`trace-participant trace-task ${scope === task.task_id ? "selected" : ""}`}
              onClick={() => changeFilter(() => setScope(task.task_id))}
            >
              <span className="trace-participant-icon trace-actor-agent">
                <Bot size={17} />
              </span>
              <span>
                <strong>
                  {taskActors.get(task.task_id)?.name ?? "Agent"}
                  <em> {index + 1}</em>
                </strong>
                <small>From {delegators.get(task.task_id) ?? "parent agent"}</small>
                <small className="trace-task-objective">{task.objective}</small>
                <Status tone={traceTone(task.status)} text={task.status} />
              </span>
            </button>
          ))}
          {!detail?.tasks.length && (
            <p className="trace-roster-empty">No delegated tasks yet.</p>
          )}
        </section>
        {attention.length > 0 && (
          <section className="trace-attention">
            <CircleAlert size={16} />
            <div>
              <strong>
                {attention.length} task{attention.length === 1 ? "" : "s"} need
                attention
              </strong>
              <p>Choose a task to inspect its questions or errors.</p>
            </div>
          </section>
        )}
        <div className="trace-legend">
          <h3>Status colors</h3>
          {(
            [
              ["active", "Running / queued"],
              ["waiting", "Waiting / needs input"],
              ["success", "Completed / delivered"],
              ["error", "Failed / error"],
            ] as [TraceTone, string][]
          ).map(([tone, label]) => (
            <span key={tone}>
              <i className={`trace-dot trace-tone-${tone}`} />
              {label}
            </span>
          ))}
          <p>
            Participant colors identify who is speaking. Status colors describe
            what happened.
          </p>
        </div>
      </aside>
      <section className="trace-main">
        <div className="trace-main-heading">
          <div>
            <span className="eyebrow">EVENT TIMELINE</span>
            <h2>
              {selectedTask
                ? (taskActors.get(scope)?.name ?? "Task activity")
                : scope === "lead"
                  ? "Blix lead’s conversations"
                  : "Follow the conversation"}
            </h2>
            <p>
              {selectedTask?.objective ??
                "Messages, handoffs, and tool activity in the order they happened."}
            </p>
          </div>
          <button
            className="secondary-button"
            onClick={() => {
              setLimit(filtered.length)
              requestAnimationFrame(() =>
                document
                  .getElementById("trace-latest")
                  ?.scrollIntoView({ behavior: "smooth", block: "end" }),
              )
            }}
          >
            Latest <ArrowDown size={14} />
          </button>
        </div>
        <div className="trace-controls">
          <div className="trace-tabs" aria-label="Trace filters">
            {[
              ["story", "Activity"],
              ["conversation", "Conversations"],
              ["errors", "Needs attention"],
              ["all", "All events"],
            ].map(([id, label]) => (
              <button
                key={id}
                aria-pressed={mode === id}
                className={mode === id ? "selected" : ""}
                onClick={() => changeFilter(() => setMode(id))}
              >
                {label}
              </button>
            ))}
          </div>
          <label className="trace-search">
            <Search size={15} />
            <input
              aria-label="Search trace"
              placeholder="Search activity…"
              value={query}
              onChange={(e) => changeFilter(() => setQuery(e.target.value))}
            />
            {query && (
              <button
                aria-label="Clear search"
                onClick={() => changeFilter(() => setQuery(""))}
              >
                <X size={13} />
              </button>
            )}
          </label>
        </div>
        <div className="trace-count">
          <span>
            {filtered.length.toLocaleString()} matching events
            {mode === "story" ? " · routine model events hidden" : ""}
          </span>
          <span>Oldest first</span>
        </div>
        <div className="trace-timeline">
          {shown.map((entry) => (
            <div className="trace-timeline-row" key={entry.event.event_id}>
              <span className={`trace-timeline-dot trace-tone-${entry.tone}`} />
              {entry.event.type === "message.user_v1" && (
                <div className="trace-turn-marker">
                  USER TURN {turnNumbers.get(entry.event.turn_id ?? "") ?? ""}
                </div>
              )}
              <TraceCard entry={entry} start={detail?.run.created_at} />
            </div>
          ))}
        </div>
        {!filtered.length && (
          <div className="empty-state">
            {events.length
              ? "No events match these filters."
              : "Waiting for the first event…"}
          </div>
        )}
        {filtered.length > shown.length && (
          <button
            className="secondary-button trace-load-more"
            onClick={() => setLimit((current) => current + 100)}
          >
            Show next {Math.min(100, filtered.length - shown.length)} events{" "}
            <ChevronDown size={14} />
          </button>
        )}
        <div id="trace-latest" className="trace-latest">
          {filtered.length > shown.length ? (
            <button onClick={() => setLimit(filtered.length)}>
              Show all {filtered.length.toLocaleString()} matching events to
              reach the latest
            </button>
          ) : (
            <span>End of trace · refreshes every 2.5 seconds</span>
          )}
        </div>
      </section>
    </div>
  )
}
