from dataclasses import dataclass
from typing import Any, Protocol

from litellm import acompletion

from blix_agent_runtime.errors import ModelGatewayError


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


class LiteLLMGateway:
    def __init__(
        self,
        *,
        model: str,
        api_base: str | None = None,
        timeout_seconds: float = 120,
        retries: int = 2,
    ) -> None:
        self.model = model
        self.api_base = api_base
        self.timeout_seconds = timeout_seconds
        self.retries = retries

    async def complete(
        self,
        *,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]],
    ) -> ModelTurn:
        parameters: dict[str, Any] = {
            "model": self.model,
            "messages": messages,
            "timeout": self.timeout_seconds,
            "num_retries": self.retries,
        }
        if tools:
            parameters["tools"] = tools
            parameters["tool_choice"] = "auto"
        if self.api_base:
            parameters["api_base"] = self.api_base

        try:
            response = await acompletion(**parameters)
            if not response.choices:
                raise ModelGatewayError("Model returned no choices")
            response_message = response.choices[0].message
        except ModelGatewayError:
            raise
        except Exception as error:
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
