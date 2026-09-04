from collections.abc import Collection, Iterable
from types import MappingProxyType

from blix_agent_worker.models.tool import ToolCall, ToolDefinition
from blix_agent_worker.tools.main import Tool


class ToolRegistryError(Exception):
    pass


class DuplicateToolError(ToolRegistryError):
    def __init__(self, tool_id: str) -> None:
        self.tool_id = tool_id
        super().__init__(f"tool {tool_id!r} is already registered")


class ToolNotFoundError(ToolRegistryError):
    def __init__(self, tool_id: str) -> None:
        self.tool_id = tool_id
        super().__init__(f"tool {tool_id!r} is not registered")


class ToolRegistry:
    def __init__(self, tools: Iterable[Tool] = ()) -> None:
        registered: dict[str, Tool] = {}
        for tool in tools:
            tool_id = tool.definition.id
            if tool_id in registered:
                raise DuplicateToolError(tool_id)
            registered[tool_id] = tool

        self._tools = MappingProxyType(registered)
        self._definitions = tuple(registered[tool_id].definition for tool_id in sorted(registered))
        self._ids = frozenset(registered)

    def __len__(self) -> int:
        return len(self._tools)

    def definitions(self, allowed_ids: Collection[str] | None = None) -> tuple[ToolDefinition, ...]:
        if allowed_ids is None:
            return self._definitions

        allowed_ids = frozenset(allowed_ids)
        unknown_ids = allowed_ids.difference(self._ids)
        if unknown_ids:
            raise ToolNotFoundError(min(unknown_ids))

        return tuple(definition for definition in self._definitions if definition.id in allowed_ids)

    def resolve(self, call: ToolCall) -> Tool:
        try:
            return self._tools[call.tool_id]
        except KeyError:
            raise ToolNotFoundError(call.tool_id) from None
