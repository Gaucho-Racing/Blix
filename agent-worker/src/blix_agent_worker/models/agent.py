from enum import StrEnum
from typing import Annotated, Literal, Self

from pydantic import Field, JsonValue, field_validator, model_validator

from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    ExecutionID,
    Identifier,
    JsonObject,
    NonEmptyText,
    RunID,
    VersionedIdentifier,
)
from blix_agent_worker.models.llm import ModelContinuation, ModelMessage


class AgentKind(StrEnum):
    ENTRYPOINT = "entrypoint"
    SPECIALIST = "specialist"


class AgentDefinition(ContractModel):
    id: VersionedIdentifier
    kind: AgentKind
    description: NonEmptyText
    instructions: NonEmptyText
    input_schema: JsonObject
    output_schema: JsonObject
    model_profile: Identifier | None = None
    tool_ids: tuple[VersionedIdentifier, ...] = ()
    subagent_ids: tuple[VersionedIdentifier, ...] = ()

    @field_validator("input_schema", "output_schema")
    @classmethod
    def schemas_must_describe_objects(cls, schema: JsonObject) -> JsonObject:
        if schema.get("type") != "object":
            raise ValueError("agent schemas must describe JSON objects")
        return schema

    @field_validator("tool_ids", "subagent_ids")
    @classmethod
    def ids_must_be_unique(cls, ids: tuple[str, ...]) -> tuple[str, ...]:
        if len(ids) != len(set(ids)):
            raise ValueError("ids must be unique")
        return ids

    @model_validator(mode="after")
    def agent_cannot_delegate_to_itself(self) -> Self:
        if self.id in self.subagent_ids:
            raise ValueError("an agent cannot delegate to itself")
        return self


class AgentTask(ContractModel):
    schema_version: Literal[1] = 1
    run_id: RunID
    execution_id: ExecutionID
    parent_execution_id: ExecutionID | None = None
    agent_id: VersionedIdentifier
    objective: NonEmptyText
    input: JsonObject = Field(default_factory=dict)
    context: JsonObject = Field(default_factory=dict)
    artifact_refs: tuple[NonEmptyText, ...] = ()
    allowed_tool_ids: tuple[VersionedIdentifier, ...] | None = None
    allowed_subagent_ids: tuple[VersionedIdentifier, ...] | None = None
    delegation_depth: Annotated[int, Field(ge=0)] = 0
    response_schema: JsonObject | None = None

    @field_validator("artifact_refs", "allowed_tool_ids", "allowed_subagent_ids")
    @classmethod
    def values_must_be_unique(cls, values: tuple[str, ...] | None) -> tuple[str, ...] | None:
        if values is not None and len(values) != len(set(values)):
            raise ValueError("values must be unique")
        return values

    @model_validator(mode="after")
    def execution_cannot_parent_itself(self) -> Self:
        if self.parent_execution_id == self.execution_id:
            raise ValueError("an agent execution cannot be its own parent")
        return self


class AgentResultStatus(StrEnum):
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    CANCELLED = "cancelled"


class AgentUsage(ContractModel):
    model_calls: Annotated[int, Field(ge=0)] = 0
    tool_calls: Annotated[int, Field(ge=0)] = 0
    delegation_calls: Annotated[int, Field(ge=0)] = 0
    input_tokens: Annotated[int, Field(ge=0)] = 0
    output_tokens: Annotated[int, Field(ge=0)] = 0


class AgentExecutionPhase(StrEnum):
    READY_FOR_MODEL = "ready_for_model"
    PROCESSING_CALLS = "processing_calls"
    WAITING_FOR_AGENT = "waiting_for_agent"
    COMPLETED = "completed"


class PendingAgentCall(ContractModel):
    request_ref: NonEmptyText
    request_index: Annotated[int, Field(ge=0)]
    task: AgentTask


class AgentExecutionState(ContractModel):
    schema_version: Literal[1] = 1
    agent_id: VersionedIdentifier
    phase: AgentExecutionPhase
    messages: Annotated[tuple[ModelMessage, ...], Field(min_length=1)]
    continuation: ModelContinuation | None = None
    usage: AgentUsage = Field(default_factory=AgentUsage)
    next_call_index: Annotated[int, Field(ge=0)] = 0
    pending_agent_call: PendingAgentCall | None = None

    @model_validator(mode="after")
    def validate_pending_agent_call(self) -> Self:
        if self.phase is AgentExecutionPhase.WAITING_FOR_AGENT and self.pending_agent_call is None:
            raise ValueError("a waiting execution must contain a pending agent call")
        if self.phase is not AgentExecutionPhase.WAITING_FOR_AGENT and self.pending_agent_call is not None:
            raise ValueError("only a waiting execution may contain a pending agent call")
        if self.pending_agent_call is not None and self.pending_agent_call.request_index != self.next_call_index:
            raise ValueError("the pending agent call must match next_call_index")
        return self


class AgentSuspension(ContractModel):
    state: AgentExecutionState
    child_task: AgentTask

    @model_validator(mode="after")
    def child_task_must_match_pending_call(self) -> Self:
        pending = self.state.pending_agent_call
        if pending is None or pending.task != self.child_task:
            raise ValueError("the suspended child task must match the pending agent call")
        return self


class AgentResult(ContractModel):
    schema_version: Literal[1] = 1
    run_id: RunID
    execution_id: ExecutionID
    parent_execution_id: ExecutionID | None = None
    agent_id: VersionedIdentifier
    status: AgentResultStatus
    output: JsonValue = None
    artifact_refs: tuple[NonEmptyText, ...] = ()
    usage: AgentUsage = Field(default_factory=AgentUsage)
    error: ContractError | None = None

    @field_validator("artifact_refs")
    @classmethod
    def artifact_refs_must_be_unique(cls, values: tuple[str, ...]) -> tuple[str, ...]:
        if len(values) != len(set(values)):
            raise ValueError("artifact_refs must be unique")
        return values

    @model_validator(mode="after")
    def validate_status_fields(self) -> Self:
        if self.status is AgentResultStatus.SUCCEEDED and self.error is not None:
            raise ValueError("a succeeded agent result cannot contain error")
        if self.status is not AgentResultStatus.SUCCEEDED and self.error is None:
            raise ValueError("an unsuccessful agent result must contain error")
        return self


type AgentRunOutcome = AgentResult | AgentSuspension
