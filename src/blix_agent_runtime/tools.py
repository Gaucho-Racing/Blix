import inspect
from collections.abc import Awaitable, Callable, Iterable, Mapping, Sequence
from datetime import datetime
from typing import Any, Literal
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from pydantic import BaseModel, ConfigDict, Field, ValidationError

from blix_agent_runtime.errors import ToolConfigurationError

ToolHandler = Callable[[BaseModel], Awaitable[Any] | Any]


class CurrentTimeInput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    timezone: str = Field(default="UTC", description="IANA timezone name, such as America/Los_Angeles")


class CalculateInput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    operation: Literal["add", "subtract", "multiply", "divide"]
    operands: list[float] = Field(min_length=2)


class DelegateTaskInput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    agent: str = Field(description="Name of the subagent to delegate to")
    task: str = Field(min_length=1, description="Self-contained task for the subagent")
    context: str | None = Field(default=None, description="Additional context needed to complete the task")


class ToolDefinition:
    def __init__(
        self,
        *,
        name: str,
        description: str,
        input_model: type[BaseModel],
        handler: ToolHandler,
    ) -> None:
        self.name = name
        self.description = description
        self.input_model = input_model
        self.handler = handler

    def as_model_tool(self) -> dict[str, Any]:
        return {
            "type": "function",
            "function": {
                "name": self.name,
                "description": self.description,
                "parameters": self.input_model.model_json_schema(),
            },
        }

    async def execute(self, arguments: Mapping[str, Any]) -> Any:
        validated = self.input_model.model_validate(arguments)
        result = self.handler(validated)
        if inspect.isawaitable(result):
            return await result
        return result


class ToolRegistry:
    def __init__(self, tools: Sequence[ToolDefinition] = ()) -> None:
        self._tools: dict[str, ToolDefinition] = {}
        for tool in tools:
            self.register(tool)

    def register(self, tool: ToolDefinition) -> None:
        if tool.name in self._tools:
            raise ToolConfigurationError(f"Tool {tool.name!r} is already registered")
        self._tools[tool.name] = tool

    def schemas(self, allowed_tools: frozenset[str] | None = None) -> list[dict[str, Any]]:
        selected: Iterable[ToolDefinition] = self._tools.values()
        if allowed_tools is not None:
            selected = (tool for tool in selected if tool.name in allowed_tools)
        return [tool.as_model_tool() for tool in selected]

    async def execute(self, name: str, arguments: Mapping[str, Any]) -> dict[str, Any]:
        tool = self._tools.get(name)
        if tool is None:
            return {"ok": False, "error": {"type": "unknown_tool", "message": f"Unknown tool: {name}"}}

        try:
            value = await tool.execute(arguments)
        except ValidationError as error:
            return {
                "ok": False,
                "error": {
                    "type": "invalid_arguments",
                    "message": "Tool arguments did not match the required schema",
                    "details": error.errors(include_url=False),
                },
            }
        except Exception as error:
            return {
                "ok": False,
                "error": {"type": "tool_failure", "message": str(error)},
            }

        return {"ok": True, "value": value}

    def names(self) -> tuple[str, ...]:
        return tuple(self._tools)


def create_default_tool_registry() -> ToolRegistry:
    return ToolRegistry(
        (
            ToolDefinition(
                name="current_time",
                description="Return the current time in a requested IANA timezone.",
                input_model=CurrentTimeInput,
                handler=_current_time,
            ),
            ToolDefinition(
                name="calculate",
                description="Perform basic arithmetic over two or more operands.",
                input_model=CalculateInput,
                handler=_calculate,
            ),
        )
    )


def delegate_tool_schema(agent_names: Sequence[str]) -> dict[str, Any]:
    schema = DelegateTaskInput.model_json_schema()
    schema["properties"]["agent"]["enum"] = list(agent_names)
    return {
        "type": "function",
        "function": {
            "name": "delegate_task",
            "description": "Delegate a focused task to a specialized subagent and return its result.",
            "parameters": schema,
        },
    }


def _current_time(arguments: BaseModel) -> dict[str, str]:
    parsed = CurrentTimeInput.model_validate(arguments)
    try:
        timezone = ZoneInfo(parsed.timezone)
    except ZoneInfoNotFoundError as error:
        raise ValueError(f"Unknown timezone: {parsed.timezone}") from error
    return {"timezone": parsed.timezone, "time": datetime.now(timezone).isoformat()}


def _calculate(arguments: BaseModel) -> dict[str, float | str]:
    parsed = CalculateInput.model_validate(arguments)
    result = parsed.operands[0]

    if parsed.operation == "add":
        result = sum(parsed.operands)
    elif parsed.operation == "subtract":
        for operand in parsed.operands[1:]:
            result -= operand
    elif parsed.operation == "multiply":
        for operand in parsed.operands[1:]:
            result *= operand
    else:
        for operand in parsed.operands[1:]:
            if operand == 0:
                raise ValueError("Cannot divide by zero")
            result /= operand

    return {"operation": parsed.operation, "result": result}
