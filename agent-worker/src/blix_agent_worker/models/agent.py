from enum import StrEnum
from typing import Annotated, Literal, Self

from pydantic import Field, JsonValue, field_validator, model_validator

from blix_agent_worker.models.common import (
    ULID,
    ContractError,
    ContractModel,
    Identifier,
    JsonObject,
    NonEmptyText,
    PositiveSeconds,
    VersionedIdentifier,
)


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


class AgentBudget(ContractModel):
    max_model_calls: Annotated[int, Field(ge=1)] = 8
    max_tool_calls: Annotated[int, Field(ge=0)] = 32
    max_delegation_calls: Annotated[int, Field(ge=0)] = 8
    max_delegation_depth: Annotated[int, Field(ge=0)] = 1
    timeout_seconds: PositiveSeconds = 120


class AgentTask(ContractModel):
    schema_version: Literal[1] = 1
    run_id: ULID
    invocation_id: ULID
    parent_invocation_id: ULID | None = None
    agent_id: VersionedIdentifier
    objective: NonEmptyText
    input: JsonObject = Field(default_factory=dict)
    context: JsonObject = Field(default_factory=dict)
    artifact_refs: tuple[NonEmptyText, ...] = ()
    allowed_tool_ids: tuple[VersionedIdentifier, ...] | None = None
    allowed_subagent_ids: tuple[VersionedIdentifier, ...] | None = None
    delegation_depth: Annotated[int, Field(ge=0)] = 0
    response_schema: JsonObject | None = None
    budget: AgentBudget = Field(default_factory=AgentBudget)

    @field_validator("artifact_refs", "allowed_tool_ids", "allowed_subagent_ids")
    @classmethod
    def values_must_be_unique(cls, values: tuple[str, ...] | None) -> tuple[str, ...] | None:
        if values is not None and len(values) != len(set(values)):
            raise ValueError("values must be unique")
        return values


class AgentResultStatus(StrEnum):
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    CANCELLED = "cancelled"
    TIMED_OUT = "timed_out"


class AgentUsage(ContractModel):
    model_calls: Annotated[int, Field(ge=0)] = 0
    tool_calls: Annotated[int, Field(ge=0)] = 0
    delegation_calls: Annotated[int, Field(ge=0)] = 0
    input_tokens: Annotated[int, Field(ge=0)] = 0
    output_tokens: Annotated[int, Field(ge=0)] = 0


class AgentResult(ContractModel):
    schema_version: Literal[1] = 1
    run_id: ULID
    invocation_id: ULID
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
