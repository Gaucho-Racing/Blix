import asyncio
from typing import Annotated, Literal

from pydantic import Field, ValidationError

from blix_agent_worker.clients.runway import ExecutionResult, RunwayClient, RunwayClientError
from blix_agent_worker.models.common import ContractError, ContractModel, ExecutionID, JsonObject
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult, ToolResultStatus


class AgentExecutionV1Input(ContractModel):
    operation: Literal["status", "await"]
    execution_id: ExecutionID
    timeout_seconds: Annotated[int, Field(ge=1, le=30)] = 20


class AgentExecutionV1Tool:
    input_model = AgentExecutionV1Input
    definition = ToolDefinition(
        id="agent_execution_v1",
        description=(
            "Check or briefly await a background child agent execution by ID. Status returns immediately. Await "
            "waits up to timeout_seconds and returns the latest status; it does not wait indefinitely."
        ),
        input_schema=AgentExecutionV1Input.model_json_schema(),
        idempotent=True,
    )

    def __init__(self, runway: RunwayClient, worker_id: str) -> None:
        self._runway = runway
        self._worker_id = worker_id

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            arguments = self.input_model.model_validate(call.arguments)
        except ValidationError:
            return self._failure(call, "invalid_arguments", "Invalid agent execution arguments.")
        try:
            result = await self._read(call, arguments)
        except RunwayClientError as error:
            return self._failure(call, "agent_execution_unavailable", str(error), retryable=True)
        except ValueError as error:
            return self._failure(call, "agent_execution_unavailable", str(error))
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_execution_id=call.parent_execution_id,
            status=ToolResultStatus.SUCCEEDED,
            output=self._output(arguments.execution_id, result),
        )

    async def _read(self, call: ToolCall, arguments: AgentExecutionV1Input) -> ExecutionResult:
        result = await self._runway.get_execution_result(
            arguments.execution_id, call.parent_execution_id, self._worker_id
        )
        if arguments.operation == "status":
            return result
        loop = asyncio.get_running_loop()
        deadline = loop.time() + arguments.timeout_seconds
        while result.status in {"idle", "queued", "running", "waiting"} and loop.time() < deadline:
            await asyncio.sleep(min(0.5, max(0, deadline - loop.time())))
            result = await self._runway.get_execution_result(
                arguments.execution_id, call.parent_execution_id, self._worker_id
            )
        return result

    @staticmethod
    def _output(execution_id: str, result: ExecutionResult) -> JsonObject:
        return {
            "execution_id": execution_id,
            "status": result.status,
            "terminal": result.status in {"succeeded", "failed", "cancelled"},
            "result": result.result.model_dump(mode="json") if result.result is not None else None,
        }

    @staticmethod
    def _failure(call: ToolCall, code: str, message: str, *, retryable: bool = False) -> ToolResult:
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_execution_id=call.parent_execution_id,
            status=ToolResultStatus.FAILED,
            error=ContractError(code=code, message=message, retryable=retryable),
        )
