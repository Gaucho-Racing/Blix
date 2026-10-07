import asyncio
from typing import Annotated, Literal

from pydantic import Field, ValidationError, model_validator

from blix_agent_worker.clients.runway import AttemptLease, ExecutionResult, RunwayClientError
from blix_agent_worker.models.common import ContractError, ContractModel, JsonObject, TaskID
from blix_agent_worker.models.task_input import QuestionAnswer, TaskAnswer
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact, ToolResult, ToolResultStatus


class AgentExecutionV1Input(ContractModel):
    operation: Literal["list", "status", "await", "cancel", "steer", "answer"]
    task_id: TaskID | None = None
    content: str = ""
    question_id: Annotated[str, Field(min_length=1, max_length=200)] | None = None
    answers: Annotated[list[QuestionAnswer], Field(max_length=10)] = Field(default_factory=list)
    timeout_seconds: Annotated[int, Field(ge=1, le=30)] = 20

    @model_validator(mode="after")
    def validate_operation(self):
        if self.operation != "list" and self.task_id is None:
            raise ValueError("task_id is required")
        if self.operation == "steer" and not self.content.strip():
            raise ValueError("content is required for steering")
        if self.operation == "answer":
            if self.question_id is None or not self.answers:
                raise ValueError("question_id and answers are required for answering a task")
            TaskAnswer(question_id=self.question_id, answers=self.answers)
        elif self.question_id is not None or self.answers:
            raise ValueError("question_id and answers are only valid for answer operations")
        return self


class AgentExecutionV1Tool:
    input_model = AgentExecutionV1Input
    definition = ToolDefinition(
        id="agent_execution_v1",
        description=(
            "List, check, cancel, steer, answer clarification questions, or briefly await child tasks by task ID. "
            "For answer, use the pending question_id and one answer per question in order. "
            "Each answer has id, selected_options (exact option labels), and text (free-form answer). "
            "Status returns immediately. Await "
            "waits up to timeout_seconds and returns the latest status; it does not wait indefinitely."
        ),
        input_schema=AgentExecutionV1Input.model_json_schema(),
        impact=ToolImpact.MUTATING,
        idempotent=True,
    )

    def __init__(self, lease: AttemptLease) -> None:
        self._lease = lease

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            arguments = self.input_model.model_validate(call.arguments)
        except ValidationError:
            return self._failure(call, "invalid_arguments", "Invalid agent execution arguments.")
        try:
            if arguments.operation in {"list", "cancel", "steer", "answer"}:
                output = await self._lease.post(
                    "task-controls",
                    {
                        **arguments.model_dump(mode="json", exclude={"timeout_seconds"}),
                        "request_ref": call.idempotency_key or call.call_id,
                    },
                )
            else:
                result = await self._read(arguments)
                output = self._output(arguments.task_id, result)
        except RunwayClientError as error:
            return self._failure(call, "agent_execution_unavailable", str(error), retryable=True)
        except ValueError as error:
            return self._failure(call, "agent_execution_unavailable", str(error))
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_session_id=call.parent_session_id,
            status=ToolResultStatus.SUCCEEDED,
            output=output,
        )

    async def _read(self, arguments: AgentExecutionV1Input) -> ExecutionResult:
        async def read() -> ExecutionResult:
            return ExecutionResult.model_validate(
                await self._lease.post("task-controls", {"operation": "status", "task_id": arguments.task_id})
            )

        result = await read()
        if arguments.operation == "status":
            return result
        loop = asyncio.get_running_loop()
        deadline = loop.time() + arguments.timeout_seconds
        while result.status in {"queued", "running", "waiting"} and loop.time() < deadline:
            await asyncio.sleep(min(0.5, max(0, deadline - loop.time())))
            result = await read()
        return result

    @staticmethod
    def _output(task_id: str, result: ExecutionResult) -> JsonObject:
        return {
            "task_id": task_id,
            "status": result.status,
            "question": result.question,
            "answer_delivery": result.answer_delivery,
            "activity": result.activity,
            "terminal": result.status in {"succeeded", "failed", "cancelled"},
            "result": result.result.model_dump(mode="json") if result.result is not None else None,
        }

    @staticmethod
    def _failure(call: ToolCall, code: str, message: str, *, retryable: bool = False) -> ToolResult:
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_session_id=call.parent_session_id,
            status=ToolResultStatus.FAILED,
            error=ContractError(code=code, message=message, retryable=retryable),
        )
