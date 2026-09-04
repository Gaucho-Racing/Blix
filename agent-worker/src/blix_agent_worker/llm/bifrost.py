import json
from typing import Literal, Self, cast

import httpx
from pydantic import BaseModel, ConfigDict, ValidationError

from blix_agent_worker.llm.main import ModelClientError
from blix_agent_worker.models.common import JsonObject
from blix_agent_worker.models.llm import (
    ModelCallRequest,
    ModelContinuation,
    ModelMessage,
    ModelMessageRole,
    ModelRequest,
    ModelResponse,
    ModelUsage,
)

type ReasoningEffort = Literal["none", "minimal", "low", "medium", "high", "xhigh", "max"]


class _BifrostModel(BaseModel):
    model_config = ConfigDict(extra="ignore")


class _BifrostOutputContent(_BifrostModel):
    type: str
    text: str | None = None
    refusal: str | None = None


class _BifrostOutputItem(_BifrostModel):
    type: str
    call_id: str | None = None
    name: str | None = None
    arguments: str | None = None
    content: tuple[_BifrostOutputContent, ...] = ()


class _BifrostUsage(_BifrostModel):
    input_tokens: int = 0
    output_tokens: int = 0


class _BifrostResponse(_BifrostModel):
    status: str
    output: tuple[_BifrostOutputItem, ...]
    usage: _BifrostUsage | None = None


class BifrostResponsesModel:
    _CONTINUATION_ADAPTER_ID = "bifrost_responses_v1"

    def __init__(
        self,
        *,
        base_url: str,
        username: str,
        password: str,
        virtual_key: str,
        model: str,
        reasoning_effort: ReasoningEffort = "medium",
    ) -> None:
        if not base_url.strip():
            raise ValueError("base_url cannot be empty")
        if not username.strip():
            raise ValueError("username cannot be empty")
        if not password:
            raise ValueError("password cannot be empty")
        if not virtual_key.strip():
            raise ValueError("virtual_key cannot be empty")
        if not model.strip():
            raise ValueError("model cannot be empty")

        self._model = model
        self._reasoning_effort = reasoning_effort
        self._client = httpx.AsyncClient(
            base_url=f"{base_url.rstrip('/')}/",
            auth=httpx.BasicAuth(username=username, password=password),
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                "x-bf-vk": virtual_key,
            },
            timeout=None,
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._client.aclose()

    async def complete(self, request: ModelRequest) -> ModelResponse:
        try:
            input_items = self._input_items(request)
            payload = self._request_payload(request, input_items)
        except ValueError as error:
            raise ModelClientError("invalid_model_request", str(error)) from error

        try:
            response = await self._client.post("v1/responses", json=payload)
        except httpx.RequestError as error:
            raise ModelClientError("model_unavailable", "The model gateway is unavailable.", retryable=True) from error

        if response.is_error:
            message = self._error_message(response)
            retryable = response.status_code == 429 or response.status_code >= 500
            raise ModelClientError("model_request_failed", message, retryable=retryable)

        try:
            body = response.json()
            parsed = _BifrostResponse.model_validate(body)
            if parsed.status != "completed":
                raise ValueError(f"model response has terminal status {parsed.status!r}")
            message = self._model_message(parsed.output)
            output_items = self._output_items(body)
            continuation = ModelContinuation(
                adapter_id=self._CONTINUATION_ADAPTER_ID,
                state={"input_items": [*input_items, *output_items]},
            )
        except (ValueError, ValidationError) as error:
            raise ModelClientError(
                "invalid_model_response",
                "The model gateway returned an invalid response.",
            ) from error

        usage = parsed.usage or _BifrostUsage()
        return ModelResponse(
            message=message,
            usage=ModelUsage(
                input_tokens=usage.input_tokens,
                output_tokens=usage.output_tokens,
            ),
            continuation=continuation,
        )

    def _request_payload(self, request: ModelRequest, input_items: list[JsonObject]) -> JsonObject:
        payload: JsonObject = {
            "model": self._model,
            "input": input_items,
            "store": False,
            "include": ["reasoning.encrypted_content"],
            "reasoning": {"effort": self._reasoning_effort},
        }
        instructions = [
            message.content
            for message in request.messages
            if message.role is ModelMessageRole.SYSTEM and message.content is not None
        ]
        if instructions:
            payload["instructions"] = "\n\n".join(instructions)
        if request.callables:
            payload["tools"] = [
                {
                    "type": "function",
                    "name": definition.id,
                    "description": definition.description,
                    "parameters": definition.input_schema,
                }
                for definition in request.callables
            ]
            payload["tool_choice"] = "auto"
        return payload

    def _input_items(self, request: ModelRequest) -> list[JsonObject]:
        if request.continuation is None:
            return self._conversation_items(request.messages)
        if request.continuation.adapter_id != self._CONTINUATION_ADAPTER_ID:
            raise ValueError("the model continuation belongs to a different adapter")

        continuation_items = request.continuation.state.get("input_items")
        if not isinstance(continuation_items, list) or not continuation_items:
            raise ValueError("the model continuation does not contain input items")
        if not all(isinstance(item, dict) for item in continuation_items):
            raise ValueError("the model continuation contains invalid input items")
        return [*cast(list[JsonObject], continuation_items), *self._call_output_items(request.messages)]

    @staticmethod
    def _conversation_items(messages: tuple[ModelMessage, ...]) -> list[JsonObject]:
        items: list[JsonObject] = []
        for message in messages:
            if message.role is ModelMessageRole.SYSTEM:
                continue
            if message.role is ModelMessageRole.CALL_OUTPUT or message.call_requests:
                raise ValueError("callable exchanges require model continuation state")
            if message.content is None:
                raise ValueError("conversation messages must contain text")
            items.append({"role": message.role.value, "content": message.content})
        if not items:
            raise ValueError("the model request does not contain conversation input")
        return items

    @staticmethod
    def _call_output_items(messages: tuple[ModelMessage, ...]) -> list[JsonObject]:
        call_request_index = next(
            (
                index
                for index in range(len(messages) - 1, -1, -1)
                if messages[index].role is ModelMessageRole.ASSISTANT and messages[index].call_requests
            ),
            None,
        )
        if call_request_index is None:
            raise ValueError("model continuation is missing its callable request message")

        call_request_refs = {request.ref for request in messages[call_request_index].call_requests}
        trailing_messages = messages[call_request_index + 1 :]
        if not trailing_messages or any(
            message.role is not ModelMessageRole.CALL_OUTPUT for message in trailing_messages
        ):
            raise ValueError("model continuation must be followed by callable output messages")
        output_refs = {message.call_request_ref for message in trailing_messages}
        if output_refs != call_request_refs or len(output_refs) != len(trailing_messages):
            raise ValueError("callable outputs do not match the pending model requests")

        return [
            {
                "type": "function_call_output",
                "call_id": cast(str, message.call_request_ref),
                "output": cast(str, message.content),
            }
            for message in trailing_messages
        ]

    @staticmethod
    def _model_message(output: tuple[_BifrostOutputItem, ...]) -> ModelMessage:
        requests: list[ModelCallRequest] = []
        text_parts: list[str] = []
        for item in output:
            if item.type == "function_call":
                if not item.call_id or not item.name or not item.arguments:
                    raise ValueError("model returned an incomplete function call")
                try:
                    arguments = json.loads(item.arguments)
                except json.JSONDecodeError as error:
                    raise ValueError("model returned malformed callable arguments") from error
                if not isinstance(arguments, dict):
                    raise ValueError("model returned non-object callable arguments")
                requests.append(
                    ModelCallRequest(
                        ref=item.call_id,
                        callable_id=item.name,
                        arguments=arguments,
                    )
                )
                continue
            if item.type != "message":
                continue
            for content in item.content:
                value = content.text if content.type == "output_text" else content.refusal
                if value and value.strip():
                    text_parts.append(value.strip())

        content = "\n\n".join(text_parts) or None
        return ModelMessage(
            role=ModelMessageRole.ASSISTANT,
            content=content,
            call_requests=tuple(requests),
        )

    @staticmethod
    def _output_items(body: object) -> list[JsonObject]:
        if not isinstance(body, dict):
            raise ValueError("model response is not an object")
        output = body.get("output")
        if not isinstance(output, list) or not output or not all(isinstance(item, dict) for item in output):
            raise ValueError("model response does not contain output items")
        return cast(list[JsonObject], output)

    @staticmethod
    def _error_message(response: httpx.Response) -> str:
        fallback = f"The model gateway returned HTTP {response.status_code}."
        try:
            body = response.json()
        except ValueError:
            return fallback
        if not isinstance(body, dict):
            return fallback
        error = body.get("error")
        if isinstance(error, dict) and isinstance(error.get("message"), str):
            return error["message"]
        if isinstance(error, str):
            return error
        return fallback
