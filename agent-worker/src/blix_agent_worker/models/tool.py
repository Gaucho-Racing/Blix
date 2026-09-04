from enum import StrEnum
from typing import Literal, Self

from pydantic import JsonValue, field_validator, model_validator

from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    JsonObject,
    NonEmptyText,
    PositiveSeconds,
    VersionedIdentifier,
)


class ToolImpact(StrEnum):
    READ_ONLY = "read_only"
    MUTATING = "mutating"
    DESTRUCTIVE = "destructive"


class ToolExecutionMode(StrEnum):
    INLINE = "inline"
    JOB = "job"


class ToolDefinition(ContractModel):
    id: VersionedIdentifier
    description: NonEmptyText
    input_schema: JsonObject
    output_schema: JsonObject | None = None
    impact: ToolImpact = ToolImpact.READ_ONLY
    execution_mode: ToolExecutionMode = ToolExecutionMode.INLINE
    timeout_seconds: PositiveSeconds = 30
    idempotent: bool = False
    requires_approval: bool = False

    @field_validator("input_schema")
    @classmethod
    def input_schema_must_describe_an_object(cls, schema: JsonObject) -> JsonObject:
        if schema.get("type") != "object":
            raise ValueError("input_schema must describe a JSON object")
        return schema


class ToolCall(ContractModel):
    schema_version: Literal[1] = 1
    call_id: NonEmptyText
    tool_id: VersionedIdentifier
    arguments: JsonObject


class ToolResultStatus(StrEnum):
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    QUEUED = "queued"


class ToolResult(ContractModel):
    schema_version: Literal[1] = 1
    call_id: NonEmptyText
    status: ToolResultStatus
    output: JsonValue = None
    error: ContractError | None = None
    job_id: NonEmptyText | None = None

    @model_validator(mode="after")
    def validate_status_fields(self) -> Self:
        if self.status is ToolResultStatus.SUCCEEDED and (self.error is not None or self.job_id is not None):
            raise ValueError("a succeeded tool result cannot contain error or job_id")
        if self.status is ToolResultStatus.FAILED and self.error is None:
            raise ValueError("a failed tool result must contain error")
        if self.status is ToolResultStatus.QUEUED and not self.job_id:
            raise ValueError("a queued tool result must contain job_id")
        if self.status is ToolResultStatus.QUEUED and self.error is not None:
            raise ValueError("a queued tool result cannot contain error")
        return self
