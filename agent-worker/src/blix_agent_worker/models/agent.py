from enum import StrEnum
from typing import Annotated, Literal, Self
from uuid import UUID

from pydantic import Field, JsonValue, field_validator, model_validator

from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    Identifier,
    JsonObject,
    NonEmptyText,
    PositiveSeconds,
)


class AgentKind(StrEnum):
    ENTRYPOINT = "entrypoint"
    SPECIALIST = "specialist"


class AgentDefinition(ContractModel):
    name: Identifier
    version: Annotated[int, Field(ge=1)] = 1
    kind: AgentKind
    description: NonEmptyText
    instructions: NonEmptyText
    model_profile: Identifier | None = None
    tool_names: tuple[Identifier, ...] = ()
    subagent_names: tuple[Identifier, ...] = ()

    @field_validator("tool_names", "subagent_names")
    @classmethod
    def names_must_be_unique(cls, names: tuple[str, ...]) -> tuple[str, ...]:
        if len(names) != len(set(names)):
            raise ValueError("names must be unique")
        return names

    @model_validator(mode="after")
    def agent_cannot_delegate_to_itself(self) -> Self:
        if self.name in self.subagent_names:
            raise ValueError("an agent cannot delegate to itself")
        return self


class AgentBudget(ContractModel):
    max_model_calls: Annotated[int, Field(ge=1)] = 8
    max_tool_calls: Annotated[int, Field(ge=0)] = 32
    max_delegation_depth: Annotated[int, Field(ge=0)] = 1
    timeout_seconds: PositiveSeconds = 120


class AgentTask(ContractModel):
    schema_version: Literal[1] = 1
    run_id: UUID
    invocation_id: UUID
    parent_invocation_id: UUID | None = None
    agent_name: Identifier
    agent_version: Annotated[int, Field(ge=1)] = 1
    objective: NonEmptyText
    input: JsonObject = Field(default_factory=dict)
    context: JsonObject = Field(default_factory=dict)
    artifact_refs: tuple[NonEmptyText, ...] = ()
    tool_allowlist: tuple[Identifier, ...] | None = None
    response_schema: JsonObject | None = None
    budget: AgentBudget = Field(default_factory=AgentBudget)

    @field_validator("artifact_refs", "tool_allowlist")
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
    input_tokens: Annotated[int, Field(ge=0)] = 0
    output_tokens: Annotated[int, Field(ge=0)] = 0


class AgentResult(ContractModel):
    schema_version: Literal[1] = 1
    run_id: UUID
    invocation_id: UUID
    agent_name: Identifier
    agent_version: Annotated[int, Field(ge=1)] = 1
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
