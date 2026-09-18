import json
import math
import os
from datetime import UTC, datetime
from email.utils import parsedate_to_datetime
from typing import Literal, Self, cast

import httpx
from pydantic import BaseModel, ConfigDict, HttpUrl, TypeAdapter, ValidationError

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


class _ResponseModel(BaseModel):
    model_config = ConfigDict(extra="ignore")


class _OutputContent(_ResponseModel):
    type: str
    text: str | None = None
    refusal: str | None = None


class _OutputItem(_ResponseModel):
    type: str
    call_id: str | None = None
    name: str | None = None
    arguments: str | None = None
    content: tuple[_OutputContent, ...] = ()


class _Usage(_ResponseModel):
    input_tokens: int = 0
    output_tokens: int = 0


class _Response(_ResponseModel):
    status: str
    output: tuple[_OutputItem, ...]
    usage: _Usage | None = None


class OpenAIResponsesModel:
    _CONTINUATION_ADAPTER_ID = "openai_responses_v1"

    def __init__(
        self,
        *,
        base_url: str,
        api_key: str,
        model: str,
        reasoning_effort: ReasoningEffort | None = None,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        try:
            endpoint = TypeAdapter(HttpUrl).validate_python(base_url.strip())
        except ValidationError as error:
            raise ValueError("LLM_GATEWAY_ENDPOINT must be an absolute HTTP(S) API base URL") from error
        if endpoint.username or endpoint.password or endpoint.query or endpoint.fragment:
            raise ValueError("LLM_GATEWAY_ENDPOINT cannot contain credentials, a query, or a fragment")
        if not api_key.strip():
            raise ValueError("LLM_GATEWAY_API_KEY cannot be empty")
        if any(ord(char) < 33 or ord(char) > 126 for char in api_key.strip()):
            raise ValueError("LLM_GATEWAY_API_KEY must contain only printable non-whitespace ASCII characters")
        if not model.strip():
            raise ValueError("LLM_MODEL cannot be empty")
        if reasoning_effort is not None:
            try:
                reasoning_effort = TypeAdapter(ReasoningEffort).validate_python(reasoning_effort)
            except ValidationError as error:
                raise ValueError("LLM_REASONING_EFFORT is invalid") from error

        self._base_url = f"{str(endpoint).rstrip('/')}/"
        self._model = model.strip()
        self._reasoning_effort = reasoning_effort
        self._client = httpx.AsyncClient(
            base_url=self._base_url,
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                "Authorization": f"Bearer {api_key.strip()}",
            },
            timeout=None,
            follow_redirects=False,
            transport=transport,
        )

    @classmethod
    def from_environment(cls) -> Self:
        return cls(
            base_url=os.environ.get("LLM_GATEWAY_ENDPOINT", ""),
            api_key=os.environ.get("LLM_GATEWAY_API_KEY", ""),
            model=os.environ.get("LLM_MODEL", "gpt-6-astra"),
            reasoning_effort=os.environ.get("LLM_REASONING_EFFORT", "").strip() or None,
        )

    @property
    def model(self) -> str:
        return self._model

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
            response = await self._client.post("responses", json=payload)
        except httpx.RequestError as error:
            retryable = isinstance(
                error, (httpx.TimeoutException, httpx.NetworkError, httpx.RemoteProtocolError, httpx.ProxyError)
            )
            raise ModelClientError(
                "model_unavailable", "The model gateway is unavailable.", retryable=retryable
            ) from error

        if not response.is_success:
            message = self._error_message(response)
            retryable = response.status_code in {408, 429} or (
                response.status_code >= 500 and response.status_code not in {501, 505}
            )
            raise ModelClientError(
                "model_request_failed",
                message,
                retryable=retryable,
                http_status=response.status_code,
                retry_after_seconds=self._retry_after(response),
            )

        try:
            body = response.json()
            parsed = _Response.model_validate(body)
            if parsed.status != "completed":
                raise ValueError(f"model response has terminal status {parsed.status!r}")
            message = self._model_message(parsed.output)
            output_items = self._output_items(body)
            continuation = ModelContinuation(
                adapter_id=self._CONTINUATION_ADAPTER_ID,
                state={"endpoint": self._base_url, "model": self._model, "input_items": [*input_items, *output_items]},
            )
        except (ValueError, ValidationError) as error:
            raise ModelClientError(
                "invalid_model_response",
                "The model gateway returned an invalid response.",
            ) from error

        usage = parsed.usage or _Usage()
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
        }
        if self._reasoning_effort is not None:
            payload["reasoning"] = {"effort": self._reasoning_effort}
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
        if request.output_format is not None:
            payload["text"] = {
                "format": {
                    "type": "json_schema",
                    "name": request.output_format.id,
                    "strict": True,
                    "schema": request.output_format.json_schema,
                }
            }
        return payload

    def _input_items(self, request: ModelRequest) -> list[JsonObject]:
        if request.continuation is None:
            return self._conversation_items(request.messages)
        if request.continuation.adapter_id != self._CONTINUATION_ADAPTER_ID:
            raise ValueError("the saved execution uses a different model adapter; start a new run")
        if (
            request.continuation.state.get("endpoint") != self._base_url
            or request.continuation.state.get("model") != self._model
        ):
            raise ValueError("the saved execution uses a different endpoint or model; start a new run")

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
    def _model_message(output: tuple[_OutputItem, ...]) -> ModelMessage:
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
    def _retry_after(response: httpx.Response) -> float | None:
        value = response.headers.get("Retry-After")
        if value is None:
            return None
        try:
            seconds = float(value)
        except ValueError:
            try:
                deadline = parsedate_to_datetime(value)
                seconds = (deadline - datetime.now(UTC)).total_seconds()
            except (TypeError, ValueError, OverflowError):
                return None
        return max(0.0, seconds) if math.isfinite(seconds) else None

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
