import {
  Activity,
  Bot,
  BrainCircuit,
  CheckCircle2,
  CircleAlert,
  CornerDownRight,
  MessageSquareText,
  MousePointerClick,
  ShieldCheck,
  UserRound,
  Wrench,
} from "lucide-react"
import { useMemo } from "react"
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
  type NodeTypes,
} from "@xyflow/react"

import { eventLabel, formatTimestamp, modelEventSummary, readString } from "@/lib/format"
import type { ExecutionEvent, ExecutionSummary } from "@/lib/types"

import "@xyflow/react/dist/style.css"

const horizontalSpacing = 292
const verticalSpacing = 94
const graphPadding = 34
const rootEventOffset = 132
const executionNodeSize = { width: 244, height: 88 }
const eventNodeSize = { width: 224, height: 76 }

interface RunDagNodeData extends Record<string, unknown> {
  kind: "execution" | "event"
  category: string
  eventType?: string
  title: string
  summary: string
  identifier: string
  executionID: string
  status?: string
  position?: number
  timestamp?: string
}

type RunDagNode = Node<RunDagNodeData, "runDag">

const eventIcons = {
  "message.user_v1": UserRound,
  "message.assistant_v1": MessageSquareText,
  "model.response_v1": BrainCircuit,
  "model.call_v1": BrainCircuit,
  "model.failure_v1": CircleAlert,
  "tool.call_v1": Wrench,
  "tool.result_v1": CheckCircle2,
  "agent.call_v1": CornerDownRight,
  "agent.result_v1": Bot,
  "execution.claimed_v1": MousePointerClick,
  "execution.failed_v1": CircleAlert,
  "caller.verified_v1": ShieldCheck,
}

function eventSummary(event: ExecutionEvent): string {
  const content = readString(event.payload, "content")
  if (content) return content

  if (event.type.startsWith("model.")) return modelEventSummary(event)

  if (event.type === "execution.failed_v1") {
    const error = event.payload.error
    if (error !== null && typeof error === "object" && !Array.isArray(error)) {
      const message = readString(error as Record<string, unknown>, "message")
      if (message) return message
    }
  }

  return ["tool_id", "agent_id", "worker_id", "status", "required_group"]
    .map((key) => readString(event.payload, key))
    .find(Boolean) ?? event.entity_id
}

function RunDagNodeView({ data, selected }: NodeProps<RunDagNode>) {
  const isExecution = data.kind === "execution"
  const Icon = isExecution
    ? Bot
    : eventIcons[data.eventType as keyof typeof eventIcons] ?? Activity

  return (
    <div
      className={`run-dag-node run-dag-${data.kind} run-dag-category-${data.category}${selected ? " selected" : ""}`}
      title={data.summary}
    >
      <Handle className="run-dag-handle" position={Position.Top} type="target" />
      <div className="run-dag-node-heading">
        <span><Icon size={13} /></span>
        <strong>{data.title}</strong>
        {isExecution ? <em>{data.status}</em> : <em>#{data.position}</em>}
      </div>
      <p>{data.summary}</p>
      <div className="run-dag-node-meta">
        <code>{data.identifier}</code>
        {data.timestamp && <time>{formatTimestamp(data.timestamp)}</time>}
      </div>
      <Handle className="run-dag-handle" position={Position.Bottom} type="source" />
    </div>
  )
}

const nodeTypes = { runDag: RunDagNodeView } satisfies NodeTypes

function executionDepth(execution: ExecutionSummary, byID: Map<string, ExecutionSummary>): number {
  let depth = 0
  let parentID = execution.parent_execution_id
  const visited = new Set<string>()
  while (parentID && !visited.has(parentID)) {
    visited.add(parentID)
    depth += 1
    parentID = byID.get(parentID)?.parent_execution_id ?? null
  }
  return depth
}

function graphElements(
  executions: ExecutionSummary[],
  events: ExecutionEvent[],
  selectedExecutionID: string,
): { nodes: RunDagNode[]; edges: Edge[] } {
  const nodes: RunDagNode[] = []
  const edges: Edge[] = []
  const edgePairs = new Set<string>()
  const eventsByExecution = new Map<string, ExecutionEvent[]>()
  const executionsByID = new Map(executions.map((execution) => [execution.execution_id, execution]))
  const sortedEvents = [...events].sort((left, right) => left.position - right.position)
  const firstPosition = sortedEvents[0]?.position ?? 0

  const eventY = (event: ExecutionEvent) => rootEventOffset + (event.position - firstPosition) * verticalSpacing
  const addEdge = (source: string, target: string, kind = "sequence") => {
    const pair = `${source}:${target}`
    if (edgePairs.has(pair)) return
    edgePairs.add(pair)
    edges.push({
      id: `edge:${pair}`,
      source,
      target,
      type: "smoothstep",
      className: `run-dag-edge run-dag-edge-${kind}`,
      markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14 },
    })
  }

  for (const event of sortedEvents) {
    const executionEvents = eventsByExecution.get(event.execution_id) ?? []
    executionEvents.push(event)
    eventsByExecution.set(event.execution_id, executionEvents)
    const execution = executionsByID.get(event.execution_id)
    const depth = execution ? executionDepth(execution, executionsByID) : 0

    nodes.push({
      id: `event:${event.event_id}`,
      type: "runDag",
      position: { x: graphPadding + depth * horizontalSpacing, y: eventY(event) },
      data: {
        kind: "event",
        category: event.type.split(".")[0],
        eventType: event.type,
        title: eventLabel(event.type),
        summary: eventSummary(event),
        identifier: event.type.startsWith("model.") ? event.entity_id : event.event_id,
        executionID: event.execution_id,
        position: event.position,
        timestamp: event.created_at,
      },
      style: eventNodeSize,
    })
  }

  for (const execution of executions) {
    const depth = executionDepth(execution, executionsByID)
    const executionEvents = eventsByExecution.get(execution.execution_id) ?? []
    const parentEvents = execution.parent_execution_id
      ? eventsByExecution.get(execution.parent_execution_id) ?? []
      : []
    const callEvent = parentEvents.find(
      (event) => event.type === "agent.call_v1" && event.entity_id === execution.execution_id,
    )
    const firstEvent = executionEvents[0]
    const anchorY = execution.parent_execution_id
      ? Math.max(graphPadding, callEvent ? eventY(callEvent) : firstEvent ? eventY(firstEvent) - verticalSpacing : graphPadding)
      : graphPadding

    nodes.push({
      id: `execution:${execution.execution_id}`,
      type: "runDag",
      position: { x: graphPadding + depth * horizontalSpacing, y: anchorY },
      selected: execution.execution_id === selectedExecutionID,
      data: {
        kind: "execution",
        category: "agent",
        title: execution.agent_id,
        summary: execution.parent_execution_id ? "Delegated agent execution" : "Root agent execution",
        identifier: execution.execution_id,
        executionID: execution.execution_id,
        status: execution.status,
      },
      style: executionNodeSize,
    })

    if (firstEvent) addEdge(`execution:${execution.execution_id}`, `event:${firstEvent.event_id}`)
    for (let index = 1; index < executionEvents.length; index += 1) {
      addEdge(`event:${executionEvents[index - 1].event_id}`, `event:${executionEvents[index].event_id}`)
    }
  }

  for (const execution of executions) {
    if (!execution.parent_execution_id) continue

    const parentEvents = eventsByExecution.get(execution.parent_execution_id) ?? []
    const childEvents = eventsByExecution.get(execution.execution_id) ?? []
    const callEvent = parentEvents.find(
      (event) => event.type === "agent.call_v1" && event.entity_id === execution.execution_id,
    )
    const resultEvent = parentEvents.find(
      (event) => event.type === "agent.result_v1" && event.entity_id === execution.execution_id,
    )

    addEdge(
      callEvent ? `event:${callEvent.event_id}` : `execution:${execution.parent_execution_id}`,
      `execution:${execution.execution_id}`,
      "delegation",
    )
    if (resultEvent && childEvents.at(-1)) {
      addEdge(`event:${childEvents.at(-1)?.event_id}`, `event:${resultEvent.event_id}`, "return")
    }
  }

  return { nodes, edges }
}

interface RunDagProps {
  executions: ExecutionSummary[]
  events: ExecutionEvent[]
  selectedExecutionID: string
  onSelectExecution: (executionID: string) => void
}

export function RunDag({ executions, events, selectedExecutionID, onSelectExecution }: RunDagProps) {
  const elements = useMemo(
    () => graphElements(executions, events, selectedExecutionID),
    [events, executions, selectedExecutionID],
  )

  if (executions.length === 0) {
    return <div className="empty-state">No execution graph is available yet.</div>
  }

  return (
    <div className="run-dag-shell">
      <ReactFlow
        colorMode="light"
        defaultViewport={{ x: 18, y: 18, zoom: 0.9 }}
        edges={elements.edges}
        maxZoom={1.7}
        minZoom={0.04}
        nodes={elements.nodes}
        nodesConnectable={false}
        nodesDraggable={false}
        nodeTypes={nodeTypes}
        onNodeClick={(_, node) => onSelectExecution(node.data.executionID as string)}
        proOptions={{ hideAttribution: true }}
      >
        <Background color="#e8e4e9" gap={22} size={1} variant={BackgroundVariant.Dots} />
        <Controls showInteractive={false} />
        <MiniMap
          maskColor="rgba(246, 245, 247, 0.72)"
          nodeColor={(node) => node.data.kind === "execution" ? "#d20a8c" : "#d9d3db"}
          pannable
          zoomable
        />
      </ReactFlow>
    </div>
  )
}
