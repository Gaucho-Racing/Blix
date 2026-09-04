import json
from typing import Literal, Self

import httpx
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from blix_agent_worker.llm.main import ModelClientError
from blix_agent_worker.models.llm import (
    ModelMessage,
    ModelMessageRole,
    ModelRequest,
    ModelResponse,
    ModelToolRequest,
    ModelUsage,
)


class _BifrostModel(BaseModel):
    model_config = ConfigDict(extra="ignore")


class _BifrostFunctionCall(_BifrostModel):
    name: str
    arguments: str


class _BifrostToolCall(_BifrostModel):
    id: str
    type: Literal["function"]
    function: _BifrostFunctionCall


class _BifrostMessage(_BifrostModel):
    content: str | None = None
    tool_calls: tuple[_BifrostToolCall, ...] | None = None


class _BifrostChoice(_BifrostModel):
    message: _BifrostMessage


class _BifrostUsage(_BifrostModel):
    prompt_tokens: int = 0
    completion_tokens: int = 0


class _BifrostResponse(_BifrostModel):
    choices: list[_BifrostChoice] = Field(min_length=1)
    usage: _BifrostUsage | None = None


class BifrostChatModel:
    def __init__(
        self,
        *,
        base_url: str,
        virtual_key: str,
        model: str,
        timeout_seconds: float = 120,
    ) -> None:
        if not base_url.strip():
            raise ValueError("base_url cannot be empty")
        if not virtual_key.strip():
            raise ValueError("virtual_key cannot be empty")
        if not model.strip():
            raise ValueError("model cannot be empty")

        self._model = model
        self._client = httpx.AsyncClient(
            base_url=f"{base_url.rstrip('/')}/",
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                "x-bf-vk": virtual_key,
            },
            timeout=httpx.Timeout(timeout_seconds),
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._client.aclose()

    async def complete(self, request: ModelRequest) -> ModelResponse:
        payload: dict[str, object] = {
            "model": self._model,
            "messages": [self._message_payload(message) for message in request.messages],
        }
        if request.tools:
            payload["tools"] = [
                {
                    "type": "function",
                    "function": {
                        "name": definition.id,
                        "description": definition.description,
                        "parameters": definition.input_schema,
                    },
                }
                for definition in request.tools
            ]
            payload["tool_choice"] = "auto"

        try:
            response = await self._client.post("v1/chat/completions", json=payload)
        except httpx.TimeoutException as error:
            raise ModelClientError("model_timeout", "The model request timed out.", retryable=True) from error
        except httpx.RequestError as error:
            raise ModelClientError("model_unavailable", "The model gateway is unavailable.", retryable=True) from error

        if response.is_error:
            message = self._error_message(response)
            retryable = response.status_code == 429 or response.status_code >= 500
            raise ModelClientError("model_request_failed", message, retryable=retryable)

        try:
            parsed = _BifrostResponse.model_validate(response.json())
            message = self._model_message(parsed.choices[0].message)
        except (ValueError, ValidationError) as error:
            raise ModelClientError(
                "invalid_model_response",
                "The model gateway returned an invalid response.",
            ) from error

        usage = parsed.usage or _BifrostUsage()
        return ModelResponse(
            message=message,
            usage=ModelUsage(
                input_tokens=usage.prompt_tokens,
                output_tokens=usage.completion_tokens,
            ),
        )

    @staticmethod
    def _message_payload(message: ModelMessage) -> dict[str, object]:
        payload: dict[str, object] = {"role": message.role.value, "content": message.content}
        if message.tool_requests:
            payload["tool_calls"] = [
                {
                    "id": request.ref,
                    "type": "function",
                    "function": {
                        "name": request.tool_id,
                        "arguments": json.dumps(request.arguments, separators=(",", ":")),
                    },
                }
                for request in message.tool_requests
            ]
        if message.tool_request_ref is not None:
            payload["tool_call_id"] = message.tool_request_ref
        return payload

    @staticmethod
    def _model_message(message: _BifrostMessage) -> ModelMessage:
        requests: list[ModelToolRequest] = []
        for tool_call in message.tool_calls or ():
            try:
                arguments = json.loads(tool_call.function.arguments)
            except json.JSONDecodeError as error:
                raise ValueError("model returned malformed tool arguments") from error
            if not isinstance(arguments, dict):
                raise ValueError("model returned non-object tool arguments")
            requests.append(
                ModelToolRequest(
                    ref=tool_call.id,
                    tool_id=tool_call.function.name,
                    arguments=arguments,
                )
            )

        content = message.content.strip() if message.content and message.content.strip() else None
        return ModelMessage(
            role=ModelMessageRole.ASSISTANT,
            content=content,
            tool_requests=tuple(requests),
        )

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
