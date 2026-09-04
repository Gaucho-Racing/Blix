from enum import StrEnum
from typing import Annotated, Self

from pydantic import Field, field_validator, model_validator

from blix_agent_worker.models.common import ContractModel, Identifier, JsonObject, NonEmptyText, VersionedIdentifier
from blix_agent_worker.models.tool import ToolDefinition


class ModelMessageRole(StrEnum):
    SYSTEM = "system"
    USER = "user"
    ASSISTANT = "assistant"
    TOOL = "tool"


class ModelToolRequest(ContractModel):
    ref: NonEmptyText
    tool_id: VersionedIdentifier
    arguments: JsonObject


class ModelMessage(ContractModel):
    role: ModelMessageRole
    content: NonEmptyText | None = None
    tool_requests: tuple[ModelToolRequest, ...] = ()
    tool_request_ref: NonEmptyText | None = None

    @field_validator("tool_requests")
    @classmethod
    def tool_request_refs_must_be_unique(cls, requests: tuple[ModelToolRequest, ...]) -> tuple[ModelToolRequest, ...]:
        refs = tuple(request.ref for request in requests)
        if len(refs) != len(set(refs)):
            raise ValueError("tool request refs must be unique")
        return requests

    @model_validator(mode="after")
    def validate_role_fields(self) -> Self:
        if self.role is ModelMessageRole.ASSISTANT:
            if self.content is None and not self.tool_requests:
                raise ValueError("an assistant message must contain content or tool requests")
            if self.tool_request_ref is not None:
                raise ValueError("an assistant message cannot contain tool_request_ref")
            return self

        if self.role is ModelMessageRole.TOOL:
            if self.content is None or self.tool_request_ref is None:
                raise ValueError("a tool message must contain content and tool_request_ref")
            if self.tool_requests:
                raise ValueError("a tool message cannot contain tool requests")
            return self

        if self.content is None:
            raise ValueError("a system or user message must contain content")
        if self.tool_requests or self.tool_request_ref is not None:
            raise ValueError("a system or user message cannot contain tool request fields")
        return self


class ModelUsage(ContractModel):
    input_tokens: Annotated[int, Field(ge=0)] = 0
    output_tokens: Annotated[int, Field(ge=0)] = 0


class ModelContinuation(ContractModel):
    adapter_id: Identifier
    state: JsonObject


class ModelRequest(ContractModel):
    messages: Annotated[tuple[ModelMessage, ...], Field(min_length=1)]
    tools: tuple[ToolDefinition, ...] = ()
    continuation: ModelContinuation | None = None


class ModelResponse(ContractModel):
    message: ModelMessage
    usage: ModelUsage = Field(default_factory=ModelUsage)
    continuation: ModelContinuation | None = None

    @model_validator(mode="after")
    def message_must_be_from_assistant(self) -> Self:
        if self.message.role is not ModelMessageRole.ASSISTANT:
            raise ValueError("a model response must contain an assistant message")
        return self
