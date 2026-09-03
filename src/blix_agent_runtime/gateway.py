import base64
from dataclasses import dataclass
from typing import Any, Protocol

from openai import AsyncOpenAI, OpenAIError

from blix_agent_runtime.errors import ConfigurationError, ModelGatewayError

BIFROST_BASE_URL = "https://bifrost.gauchoracing.com/v1"
MODEL_NAME = "openai/gpt-4"


@dataclass(frozen=True, slots=True)
class ToolCall:
    id: str
    name: str
    arguments: str


@dataclass(frozen=True, slots=True)
class ModelTurn:
    message: dict[str, Any]
    text: str
    tool_calls: tuple[ToolCall, ...]
    usage: dict[str, Any]


class ModelGateway(Protocol):
    async def complete(
        self,
        *,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]],
    ) -> ModelTurn: ...


class BifrostGateway:
    def __init__(
        self,
        *,
        username: str,
        password: str,
        virtual_key: str,
        timeout_seconds: float = 120,
    ) -> None:
        if not username:
            raise ConfigurationError("Bifrost username cannot be empty")
        if ":" in username:
            raise ConfigurationError("Bifrost username cannot contain a colon")
        if not password:
            raise ConfigurationError("Bifrost password cannot be empty")
        if not virtual_key:
            raise ConfigurationError("Bifrost virtual key cannot be empty")

        credentials = base64.b64encode(f"{username}:{password}".encode()).decode("ascii")
        self._client = AsyncOpenAI(
            base_url=BIFROST_BASE_URL,
            api_key="unused",
            default_headers={
                "Authorization": f"Basic {credentials}",
                "x-bf-vk": virtual_key,
            },
            timeout=timeout_seconds,
            max_retries=0,
        )

    async def close(self) -> None:
        await self._client.close()

    async def complete(
        self,
        *,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]],
    ) -> ModelTurn:
        parameters: dict[str, Any] = {
            "model": MODEL_NAME,
            "messages": messages,
        }
        if tools:
            parameters["tools"] = tools
            parameters["tool_choice"] = "auto"

        try:
            response = await self._client.chat.completions.create(**parameters)
            if not response.choices:
                raise ModelGatewayError("Model returned no choices")
            response_message = response.choices[0].message
        except ModelGatewayError:
            raise
        except OpenAIError as error:
            raise ModelGatewayError(f"Model request failed: {error}") from error

        message = response_message.model_dump(exclude_none=True)
        raw_tool_calls = response_message.tool_calls or ()
        tool_calls = tuple(
            ToolCall(
                id=tool_call.id,
                name=tool_call.function.name,
                arguments=tool_call.function.arguments,
            )
            for tool_call in raw_tool_calls
        )
        usage = response.usage.model_dump(exclude_none=True) if response.usage else {}
        return ModelTurn(
            message=message,
            text=_normalize_content(response_message.content),
            tool_calls=tool_calls,
            usage=usage,
        )


def _normalize_content(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        text_parts: list[str] = []
        for part in content:
            if isinstance(part, str):
                text_parts.append(part)
            elif isinstance(part, dict) and isinstance(part.get("text"), str):
                text_parts.append(part["text"])
        return "".join(text_parts)
    return str(content)
