import json
from collections.abc import Awaitable, Callable
from typing import ClassVar

from pydantic import ValidationError

from blix_agent_worker.agents.callables import CallableCatalog, CallableNotFoundError
from blix_agent_worker.llm import LanguageModel, ModelClientError
from blix_agent_worker.models.agent import (
    AgentDefinition,
    AgentExecutionPhase,
    AgentExecutionState,
    AgentResult,
    AgentResultStatus,
    AgentRunOutcome,
    AgentTask,
    AgentUsage,
)
from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    JsonObject,
    new_ulid,
    validation_error_details,
)
from blix_agent_worker.models.llm import (
    ModelCallableKind,
    ModelContinuation,
    ModelMessage,
    ModelMessageRole,
    ModelOutputFormat,
    ModelRequest,
    ModelResponse,
)
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult, ToolResultStatus
from blix_agent_worker.tools import ToolRunner


class ToolAgent:
    definition: ClassVar[AgentDefinition]
    input_model: ClassVar[type[ContractModel]]
    output_model: ClassVar[type[ContractModel]]

    def initial_messages(self, task: AgentTask, agent_input: ContractModel) -> list[ModelMessage]:
        raise NotImplementedError

    def tool_definitions(self, agent_input: ContractModel) -> tuple[ToolDefinition, ...]:
        return self._tool_runner.registry.definitions()

    def validate_output(self, task: AgentTask, output: ContractModel, messages: list[ModelMessage]) -> None:
        pass

    def __init__(
        self,
        *,
        model: LanguageModel,
        tool_runner: ToolRunner,
        on_tool_result: Callable[[ToolCall, ToolResult, AgentExecutionState], Awaitable[None]] | None = None,
        on_model_response: Callable[[int, ModelResponse, AgentExecutionState], Awaitable[None]] | None = None,
    ) -> None:
        self._model = model
        self._tool_runner = tool_runner
        self._on_tool_result = on_tool_result
        self._on_model_response = on_model_response

    async def run(
        self,
        task: AgentTask,
        *,
        state: AgentExecutionState | None = None,
        resume_result: AgentResult | None = None,
    ) -> AgentRunOutcome:
        if task.agent_id != self.definition.id:
            return self._failure(task, "agent_mismatch", "The task targets a different agent.")

        try:
            agent_input = self.input_model.model_validate(task.input)
        except ValidationError as error:
            return self._failure(
                task,
                "invalid_agent_input",
                f"The {self.definition.id} input is invalid.",
                details=validation_error_details(error),
            )

        tool_definitions = self.tool_definitions(agent_input)
        allowed_tool_ids = frozenset(definition.id for definition in tool_definitions)
        catalog = CallableCatalog(tools=tool_definitions)

        if state is None:
            messages = self.initial_messages(task, agent_input)
            phase = AgentExecutionPhase.READY_FOR_MODEL
            continuation: ModelContinuation | None = None
            usage = AgentUsage()
            next_call_index = 0
        else:
            if state.agent_id != task.agent_id:
                return self._failure(task, "invalid_execution_state", "The execution state belongs to another agent.")
            messages = list(state.messages)
            phase = state.phase
            continuation = state.continuation
            usage = state.usage
            next_call_index = state.next_call_index

        if phase is AgentExecutionPhase.WAITING_FOR_AGENT or resume_result is not None:
            return self._failure(
                task,
                "invalid_execution_state",
                "This specialist cannot resume an agent delegation.",
                usage=usage,
            )

        while True:
            if phase is AgentExecutionPhase.COMPLETED:
                response_message = messages[-1]
                if response_message.content is None or response_message.call_requests:
                    return self._failure(
                        task,
                        "invalid_execution_state",
                        "The completed execution state has no final model message.",
                        usage=usage,
                    )
                try:
                    output = self.output_model.model_validate_json(response_message.content)
                    self.validate_output(task, output, messages)
                except ValidationError as error:
                    return self._failure(
                        task,
                        "invalid_agent_output",
                        f"The model returned an invalid {self.definition.id} result.",
                        details=validation_error_details(error),
                        usage=usage,
                    )
                except ValueError as error:
                    return self._failure(task, "invalid_agent_output", str(error), usage=usage)
                return AgentResult(
                    run_id=task.run_id,
                    execution_id=task.execution_id,
                    parent_execution_id=task.parent_execution_id,
                    agent_id=self.definition.id,
                    status=AgentResultStatus.SUCCEEDED,
                    output=output.model_dump(mode="json"),
                    usage=usage,
                )

            if phase is AgentExecutionPhase.READY_FOR_MODEL:
                model_calls = usage.model_calls + 1
                try:
                    response = await self._model.complete(
                        ModelRequest(
                            messages=tuple(messages),
                            callables=catalog.definitions,
                            output_format=ModelOutputFormat(
                                id=self.definition.id,
                                json_schema=self.definition.output_schema,
                            ),
                            continuation=continuation,
                        )
                    )
                except ModelClientError as error:
                    return self._failure(
                        task,
                        error.code,
                        str(error),
                        retryable=error.retryable,
                        usage=usage.model_copy(update={"model_calls": model_calls}),
                    )

                usage = AgentUsage(
                    model_calls=model_calls,
                    tool_calls=usage.tool_calls,
                    delegation_calls=usage.delegation_calls,
                    input_tokens=usage.input_tokens + response.usage.input_tokens,
                    output_tokens=usage.output_tokens + response.usage.output_tokens,
                )
                messages.append(response.message)
                continuation = response.continuation
                phase = (
                    AgentExecutionPhase.PROCESSING_CALLS
                    if response.message.call_requests
                    else AgentExecutionPhase.COMPLETED
                )
                next_call_index = 0
                checkpoint_state = self._state(messages, continuation, usage, phase, next_call_index)
                if self._on_model_response is not None:
                    await self._on_model_response(model_calls, response, checkpoint_state)
                continue

            call_batch = self._latest_call_batch(messages)
            if call_batch is None or next_call_index >= len(call_batch.call_requests):
                phase = AgentExecutionPhase.READY_FOR_MODEL
                next_call_index = 0
                continue
            model_call_request = call_batch.call_requests[next_call_index]
            try:
                callable_kind = catalog.resolve_kind(model_call_request.callable_id)
            except CallableNotFoundError:
                return self._failure(
                    task,
                    "unknown_model_callable",
                    f"The model requested unavailable callable {model_call_request.callable_id!r}.",
                    usage=usage,
                )
            if callable_kind is not ModelCallableKind.TOOL:
                return self._failure(
                    task,
                    "invalid_model_callable",
                    "This specialist may only call tools.",
                    usage=usage,
                )

            usage = usage.model_copy(update={"tool_calls": usage.tool_calls + 1})
            call = ToolCall(
                run_id=task.run_id,
                call_id=new_ulid("call"),
                parent_execution_id=task.execution_id,
                tool_id=model_call_request.callable_id,
                arguments=model_call_request.arguments,
            )
            if model_call_request.callable_id not in allowed_tool_ids:
                tool_result = ToolResult(
                    run_id=call.run_id,
                    call_id=call.call_id,
                    parent_execution_id=call.parent_execution_id,
                    status=ToolResultStatus.FAILED,
                    error=ContractError(
                        code="tool_not_allowed",
                        message=f"Tool {call.tool_id!r} is not allowed for this execution.",
                    ),
                )
            else:
                tool_result = await self._tool_runner.execute(call)

            messages.append(
                ModelMessage(
                    role=ModelMessageRole.CALL_OUTPUT,
                    content=json.dumps(tool_result.model_dump(mode="json"), separators=(",", ":")),
                    call_request_ref=model_call_request.ref,
                )
            )
            next_call_index += 1
            phase = (
                AgentExecutionPhase.PROCESSING_CALLS
                if next_call_index < len(call_batch.call_requests)
                else AgentExecutionPhase.READY_FOR_MODEL
            )
            checkpoint_state = self._state(
                messages,
                continuation,
                usage,
                phase,
                next_call_index if phase is AgentExecutionPhase.PROCESSING_CALLS else 0,
            )
            if self._on_tool_result is not None:
                await self._on_tool_result(call, tool_result, checkpoint_state)
            if (
                tool_result.status is ToolResultStatus.FAILED
                and tool_result.error is not None
                and tool_result.error.code == "tool_not_configured"
            ):
                return self._failure(
                    task,
                    tool_result.error.code,
                    tool_result.error.message,
                    retryable=tool_result.error.retryable,
                    details=tool_result.error.details,
                    usage=usage,
                )

    def _state(
        self,
        messages: list[ModelMessage],
        continuation: ModelContinuation | None,
        usage: AgentUsage,
        phase: AgentExecutionPhase,
        next_call_index: int,
    ) -> AgentExecutionState:
        return AgentExecutionState(
            agent_id=self.definition.id,
            phase=phase,
            messages=tuple(messages),
            continuation=continuation,
            usage=usage,
            next_call_index=next_call_index,
        )

    @staticmethod
    def _latest_call_batch(messages: list[ModelMessage]) -> ModelMessage | None:
        return next(
            (
                message
                for message in reversed(messages)
                if message.role is ModelMessageRole.ASSISTANT and message.call_requests
            ),
            None,
        )

    @staticmethod
    def _failure(
        task: AgentTask,
        code: str,
        message: str,
        *,
        retryable: bool = False,
        details: JsonObject | None = None,
        usage: AgentUsage | None = None,
    ) -> AgentResult:
        return AgentResult(
            run_id=task.run_id,
            execution_id=task.execution_id,
            parent_execution_id=task.parent_execution_id,
            agent_id=task.agent_id,
            status=AgentResultStatus.FAILED,
            usage=usage or AgentUsage(),
            error=ContractError(
                code=code,
                message=message,
                retryable=retryable,
                details=details or {},
            ),
        )
