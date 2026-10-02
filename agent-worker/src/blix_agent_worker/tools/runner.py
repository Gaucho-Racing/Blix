from collections.abc import Awaitable, Callable

from blix_agent_worker.models.common import ContractError
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolExecutionMode, ToolResult, ToolResultStatus
from blix_agent_worker.tools.registry import ToolNotFoundError, ToolRegistry


class ToolRunner:
    def __init__(
        self,
        registry: ToolRegistry,
        *,
        authorize: Callable[[ToolCall, ToolDefinition], Awaitable[ToolResult | None]] | None = None,
        complete: Callable[[ToolCall, ToolResult], Awaitable[None]] | None = None,
    ) -> None:
        self.registry = registry
        self._authorize = authorize
        self._complete = complete

    @staticmethod
    def failure(call: ToolCall, code: str, message: str) -> ToolResult:
        return ToolResult(
            conversation_id=call.conversation_id,
            call_id=call.call_id,
            parent_session_id=call.parent_session_id,
            status=ToolResultStatus.FAILED,
            error=ContractError(code=code, message=message),
        )

    async def execute(self, call: ToolCall, *, approved: bool = False) -> ToolResult:
        try:
            tool = self.registry.resolve(call)
        except ToolNotFoundError:
            return self.failure(call, "tool_not_found", f"Tool {call.tool_id!r} is not registered.")
        if self._authorize is not None:
            replay = await self._authorize(call, tool.definition)
            if replay is not None:
                return replay
        elif tool.definition.requires_approval and not approved:
            return self.failure(call, "approval_required", f"Tool {call.tool_id!r} requires approval.")
        try:
            result = await tool.execute(call)
        except Exception:
            result = self.failure(call, "tool_execution_failed", f"Tool {call.tool_id!r} failed unexpectedly.")
        if (
            result.conversation_id != call.conversation_id
            or result.call_id != call.call_id
            or result.parent_session_id != call.parent_session_id
        ):
            result = self.failure(
                call, "invalid_tool_result", f"Tool {call.tool_id!r} returned mismatched execution metadata."
            )
        if result.status is ToolResultStatus.QUEUED and tool.definition.execution_mode is not ToolExecutionMode.JOB:
            result = self.failure(call, "invalid_tool_result", "An inline tool returned a queued result.")
        if self._complete is not None:
            await self._complete(call, result)
        return result
