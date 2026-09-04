from collections.abc import Collection, Iterable
from types import MappingProxyType

from blix_agent_worker.models.tool import ToolCall, ToolDefinition
from blix_agent_worker.tools.main import Tool

type ToolKey = tuple[str, int]


class ToolRegistryError(Exception):
    pass


class DuplicateToolError(ToolRegistryError):
    def __init__(self, name: str, version: int) -> None:
        self.name = name
        self.version = version
        super().__init__(f"tool {name!r} version {version} is already registered")


class ToolNotFoundError(ToolRegistryError):
    def __init__(self, name: str, version: int | None = None) -> None:
        self.name = name
        self.version = version
        if version is None:
            message = f"tool {name!r} is not registered"
        else:
            message = f"tool {name!r} version {version} is not registered"
        super().__init__(message)


class ToolRegistry:
    def __init__(self, tools: Iterable[Tool] = ()) -> None:
        registered: dict[ToolKey, Tool] = {}
        for tool in tools:
            key = (tool.definition.name, tool.definition.version)
            if key in registered:
                raise DuplicateToolError(*key)
            registered[key] = tool

        self._tools = MappingProxyType(registered)
        self._definitions = tuple(registered[key].definition for key in sorted(registered))
        self._names = frozenset(name for name, _ in registered)

    def __len__(self) -> int:
        return len(self._tools)

    def definitions(self, allowlist: Collection[str] | None = None) -> tuple[ToolDefinition, ...]:
        if allowlist is None:
            return self._definitions

        allowed_names = frozenset(allowlist)
        unknown_names = allowed_names.difference(self._names)
        if unknown_names:
            raise ToolNotFoundError(min(unknown_names))

        return tuple(definition for definition in self._definitions if definition.name in allowed_names)

    def resolve(self, call: ToolCall) -> Tool:
        key = (call.name, call.version)
        try:
            return self._tools[key]
        except KeyError:
            raise ToolNotFoundError(*key) from None
