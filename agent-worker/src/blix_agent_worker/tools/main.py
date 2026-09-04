from typing import Protocol

from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult


class Tool(Protocol):
    definition: ToolDefinition

    async def execute(self, call: ToolCall) -> ToolResult: ...
