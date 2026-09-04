from enum import StrEnum
from typing import Annotated, Self

from pydantic import Field, field_validator, model_validator

from blix_agent_worker.models.common import ContractModel, Identifier, JsonObject, NonEmptyText, VersionedIdentifier


class ModelMessageRole(StrEnum):
    SYSTEM = "system"
    USER = "user"
    ASSISTANT = "assistant"
    CALL_OUTPUT = "call_output"


class ModelCallableKind(StrEnum):
    TOOL = "tool"
    AGENT = "agent"


class ModelCallableDefinition(ContractModel):
    id: VersionedIdentifier
    kind: ModelCallableKind
    description: NonEmptyText
    input_schema: JsonObject

    @field_validator("input_schema")
    @classmethod
    def input_schema_must_describe_an_object(cls, schema: JsonObject) -> JsonObject:
        if schema.get("type") != "object":
            raise ValueError("input_schema must describe a JSON object")
        return schema


class ModelCallRequest(ContractModel):
    ref: NonEmptyText
    callable_id: VersionedIdentifier
    arguments: JsonObject


class ModelMessage(ContractModel):
    role: ModelMessageRole
    content: NonEmptyText | None = None
    call_requests: tuple[ModelCallRequest, ...] = ()
    call_request_ref: NonEmptyText | None = None

    @field_validator("call_requests")
    @classmethod
    def call_request_refs_must_be_unique(cls, requests: tuple[ModelCallRequest, ...]) -> tuple[ModelCallRequest, ...]:
        refs = tuple(request.ref for request in requests)
        if len(refs) != len(set(refs)):
            raise ValueError("call request refs must be unique")
        return requests

    @model_validator(mode="after")
    def validate_role_fields(self) -> Self:
        if self.role is ModelMessageRole.ASSISTANT:
            if self.content is None and not self.call_requests:
                raise ValueError("an assistant message must contain content or call requests")
            if self.call_request_ref is not None:
                raise ValueError("an assistant message cannot contain call_request_ref")
            return self

        if self.role is ModelMessageRole.CALL_OUTPUT:
            if self.content is None or self.call_request_ref is None:
                raise ValueError("a call output message must contain content and call_request_ref")
            if self.call_requests:
                raise ValueError("a call output message cannot contain call requests")
            return self

        if self.content is None:
            raise ValueError("a system or user message must contain content")
        if self.call_requests or self.call_request_ref is not None:
            raise ValueError("a system or user message cannot contain call request fields")
        return self


class ModelUsage(ContractModel):
    input_tokens: Annotated[int, Field(ge=0)] = 0
    output_tokens: Annotated[int, Field(ge=0)] = 0


class ModelContinuation(ContractModel):
    adapter_id: Identifier
    state: JsonObject


class ModelRequest(ContractModel):
    messages: Annotated[tuple[ModelMessage, ...], Field(min_length=1)]
    callables: tuple[ModelCallableDefinition, ...] = ()
    continuation: ModelContinuation | None = None

    @field_validator("callables")
    @classmethod
    def callable_ids_must_be_unique(
        cls,
        definitions: tuple[ModelCallableDefinition, ...],
    ) -> tuple[ModelCallableDefinition, ...]:
        ids = tuple(definition.id for definition in definitions)
        if len(ids) != len(set(ids)):
            raise ValueError("callable IDs must be unique")
        return definitions


class ModelResponse(ContractModel):
    message: ModelMessage
    usage: ModelUsage = Field(default_factory=ModelUsage)
    continuation: ModelContinuation | None = None

    @model_validator(mode="after")
    def message_must_be_from_assistant(self) -> Self:
        if self.message.role is not ModelMessageRole.ASSISTANT:
            raise ValueError("a model response must contain an assistant message")
        return self
