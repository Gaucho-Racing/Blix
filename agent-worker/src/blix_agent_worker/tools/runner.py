from blix_agent_worker.models.common import ContractError
from blix_agent_worker.models.tool import ToolCall, ToolExecutionMode, ToolResult, ToolResultStatus
from blix_agent_worker.tools.registry import ToolNotFoundError, ToolRegistry


class ToolRunner:
    def __init__(self, registry: ToolRegistry) -> None:
        self.registry = registry

    async def execute(self, call: ToolCall, *, approved: bool = False) -> ToolResult:
        try:
            tool = self.registry.resolve(call)
        except ToolNotFoundError:
            return ToolResult(
                run_id=call.run_id,
                call_id=call.call_id,
                parent_execution_id=call.parent_execution_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="tool_not_found",
                    message=f"Tool {call.tool_id!r} is not registered.",
                ),
            )

        if tool.definition.requires_approval and not approved:
            return ToolResult(
                run_id=call.run_id,
                call_id=call.call_id,
                parent_execution_id=call.parent_execution_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="approval_required",
                    message=f"Tool {call.tool_id!r} requires approval.",
                ),
            )

        try:
            result = await tool.execute(call)
        except Exception:
            return ToolResult(
                run_id=call.run_id,
                call_id=call.call_id,
                parent_execution_id=call.parent_execution_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="tool_execution_failed",
                    message=f"Tool {call.tool_id!r} failed unexpectedly.",
                ),
            )

        if (
            result.run_id != call.run_id
            or result.call_id != call.call_id
            or result.parent_execution_id != call.parent_execution_id
        ):
            return ToolResult(
                run_id=call.run_id,
                call_id=call.call_id,
                parent_execution_id=call.parent_execution_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="invalid_tool_result",
                    message=f"Tool {call.tool_id!r} returned mismatched execution metadata.",
                ),
            )
        if result.status is ToolResultStatus.QUEUED and tool.definition.execution_mode is not ToolExecutionMode.JOB:
            return ToolResult(
                run_id=call.run_id,
                call_id=call.call_id,
                parent_execution_id=call.parent_execution_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="invalid_tool_result",
                    message=f"Inline tool {call.tool_id!r} returned a queued result.",
                ),
            )
        return result
