import asyncio
import json
from collections.abc import Awaitable, Callable, Collection, Iterator
from typing import ClassVar
from urllib.parse import urldefrag

from pydantic import JsonValue, ValidationError

from blix_agent_worker.agents.callables import (
    CallableCatalog,
    CallableCatalogError,
    CallableNotFoundError,
    restrict_callable_ids,
)
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
    ModelCallRequest,
    ModelContinuation,
    ModelMessage,
    ModelMessageRole,
    ModelOutputFormat,
    ModelRequest,
    ModelResponse,
)
from blix_agent_worker.models.tool import (
    ToolCall,
    ToolDefinition,
    ToolExecutionMode,
    ToolImpact,
    ToolResult,
    ToolResultStatus,
)
from blix_agent_worker.tools import ToolRunner
from blix_agent_worker.tools.registry import ToolRegistryError

OUTPUT_REPAIR_PROMPT = """Your final output was rejected and has not been returned to the caller.

Problem: {problem}

Fix the problem and return the complete corrected final output. Only call tools if you need evidence to fix it."""

FINALIZE_PROMPT = """You have reached this task's tool call limit, so no more tools are available.
Return your final output now using only the evidence you have already retrieved, and state what remains unverified."""


class AgentOutputValidationError(ValueError):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


def normalize_url(url: str) -> str:
    return urldefrag(url.strip())[0].rstrip("/")


def successful_tool_outputs(
    messages: list[ModelMessage], tool_ids: Collection[str]
) -> Iterator[tuple[ModelCallRequest, JsonValue]]:
    requests = {request.ref: request for message in messages for request in message.call_requests}
    for message in messages:
        if message.role is not ModelMessageRole.CALL_OUTPUT or message.call_request_ref not in requests:
            continue
        request = requests[message.call_request_ref]
        if request.callable_id not in tool_ids:
            continue
        result = ToolResult.model_validate_json(message.content)
        if result.status is ToolResultStatus.SUCCEEDED:
            yield request, result.output


def retrieved_urls(messages: list[ModelMessage], tool_ids: Collection[str]) -> frozenset[str]:
    def urls(value: JsonValue) -> Iterator[str]:
        if isinstance(value, dict):
            for key, item in value.items():
                if key == "url" and isinstance(item, str):
                    yield normalize_url(item)
                else:
                    yield from urls(item)
        elif isinstance(value, list):
            for item in value:
                yield from urls(item)

    return frozenset(url for _, output in successful_tool_outputs(messages, tool_ids) for url in urls(output))


class ToolAgent:
    definition: ClassVar[AgentDefinition]
    input_model: ClassVar[type[ContractModel]]
    output_model: ClassVar[type[ContractModel]]
    max_tool_calls: ClassVar[int | None] = None
    max_output_repairs: ClassVar[int] = 2

    def initial_messages(self, task: AgentTask, agent_input: ContractModel) -> list[ModelMessage]:
        raise NotImplementedError

    def tool_definitions(self, agent_input: ContractModel) -> tuple[ToolDefinition, ...]:
        return self._tool_runner.registry.definitions(self.definition.tool_ids)

    def validate_output(self, task: AgentTask, output: ContractModel, messages: list[ModelMessage]) -> None:
        pass

    def __init__(
        self,
        *,
        model: LanguageModel,
        tool_runner: ToolRunner,
        on_before_model: Callable[[ModelRequest], Awaitable[ModelRequest]] | None = None,
        on_tool_result: Callable[[ToolCall, ToolResult, AgentExecutionState], Awaitable[None]] | None = None,
        on_model_response: Callable[[int, ModelResponse, AgentExecutionState], Awaitable[None]] | None = None,
    ) -> None:
        self._model = model
        self._on_before_model = on_before_model
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

        permitted_ids = frozenset(restrict_callable_ids(self.definition.tool_ids, task.allowed_tool_ids))
        try:
            tool_definitions = tuple(
                definition for definition in self.tool_definitions(agent_input) if definition.id in permitted_ids
            )
            catalog = CallableCatalog(tools=tool_definitions)
        except (CallableCatalogError, ToolRegistryError) as error:
            return self._failure(task, "agent_configuration_error", str(error))
        definitions_by_id = {definition.id: definition for definition in tool_definitions}

        if state is None:
            messages = self.initial_messages(task, agent_input)
            phase = AgentExecutionPhase.READY_FOR_MODEL
            continuation: ModelContinuation | None = None
            usage = AgentUsage()
            next_call_index = 0
            output_repairs = 0
            finalize_requested = False
        else:
            if state.agent_id != task.agent_id:
                return self._failure(task, "invalid_execution_state", "The execution state belongs to another agent.")
            messages = list(state.messages)
            phase = state.phase
            continuation = state.continuation
            usage = state.usage
            next_call_index = state.next_call_index
            output_repairs = state.output_repairs
            finalize_requested = state.finalize_requested

        if phase is AgentExecutionPhase.WAITING_FOR_AGENT or resume_result is not None:
            return self._failure(
                task,
                "invalid_execution_state",
                "This specialist cannot resume an agent delegation.",
                usage=usage,
            )

        def snapshot(current_phase: AgentExecutionPhase, call_index: int) -> AgentExecutionState:
            return AgentExecutionState(
                agent_id=self.definition.id,
                phase=current_phase,
                messages=tuple(messages),
                continuation=continuation,
                usage=usage,
                next_call_index=call_index,
                output_repairs=output_repairs,
                finalize_requested=finalize_requested,
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
                except AgentOutputValidationError as error:
                    return self._failure(task, error.code, str(error), usage=usage)
                except (ValidationError, ValueError) as error:
                    details = validation_error_details(error) if isinstance(error, ValidationError) else None
                    if output_repairs < self.max_output_repairs:
                        output_repairs += 1
                        problem = json.dumps(details, separators=(",", ":")) if details else str(error)
                        repair_prompt = OUTPUT_REPAIR_PROMPT.format(problem=problem)
                        messages.append(ModelMessage(role=ModelMessageRole.USER, content=repair_prompt))
                        phase = AgentExecutionPhase.READY_FOR_MODEL
                        continue
                    if details is not None:
                        return self._failure(
                            task,
                            "invalid_agent_output",
                            f"The model returned an invalid {self.definition.id} result.",
                            details=details,
                            usage=usage,
                        )
                    return self._failure(task, "invalid_agent_output", str(error), usage=usage)
                return AgentResult(
                    run_id=task.run_id,
                    session_id=task.session_id,
                    parent_session_id=task.parent_session_id,
                    agent_id=self.definition.id,
                    status=AgentResultStatus.SUCCEEDED,
                    output=output.model_dump(mode="json"),
                    usage=usage,
                )

            if phase is AgentExecutionPhase.READY_FOR_MODEL:
                budget_exhausted = self._tool_budget_remaining(usage) == 0
                if budget_exhausted and not finalize_requested:
                    messages.append(ModelMessage(role=ModelMessageRole.USER, content=FINALIZE_PROMPT))
                    finalize_requested = True
                model_calls = usage.model_calls + 1
                try:
                    request = ModelRequest(
                        sequence=model_calls,
                        messages=tuple(messages),
                        callables=() if budget_exhausted else catalog.definitions,
                        output_format=ModelOutputFormat(
                            id=self.definition.id,
                            json_schema=self.definition.output_schema,
                        ),
                        continuation=continuation,
                    )
                    if self._on_before_model is not None:
                        request = await self._on_before_model(request)
                    messages = list(request.messages)
                    continuation = request.continuation
                    response = await self._model.complete(request)
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
                if self._on_model_response is not None:
                    await self._on_model_response(model_calls, response, snapshot(phase, next_call_index))
                continue

            call_batch = self._latest_call_batch(messages)
            if call_batch is None or next_call_index >= len(call_batch.call_requests):
                phase = AgentExecutionPhase.READY_FOR_MODEL
                next_call_index = 0
                continue

            pending_requests = call_batch.call_requests[next_call_index:]
            if not self._can_run_concurrently(pending_requests, definitions_by_id):
                pending_requests = pending_requests[:1]
            for model_call_request in pending_requests:
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

            calls = [
                ToolCall(
                    run_id=task.run_id,
                    call_id=new_ulid("call"),
                    parent_session_id=task.session_id,
                    tool_id=model_call_request.callable_id,
                    arguments=model_call_request.arguments,
                    idempotency_key=model_call_request.ref,
                )
                for model_call_request in pending_requests
            ]
            remaining_budget = self._tool_budget_remaining(usage)
            tool_results = await self._execute_tools(
                [
                    (call, call.tool_id in definitions_by_id, remaining_budget is None or index < remaining_budget)
                    for index, call in enumerate(calls)
                ]
            )

            for model_call_request, call, tool_result in zip(pending_requests, calls, tool_results, strict=True):
                usage = usage.model_copy(update={"tool_calls": usage.tool_calls + 1})
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
                if self._on_tool_result is not None:
                    await self._on_tool_result(
                        call,
                        tool_result,
                        snapshot(phase, next_call_index if phase is AgentExecutionPhase.PROCESSING_CALLS else 0),
                    )
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

    def _tool_budget_remaining(self, usage: AgentUsage) -> int | None:
        if self.max_tool_calls is None:
            return None
        return max(0, self.max_tool_calls - usage.tool_calls)

    @staticmethod
    def _can_run_concurrently(
        requests: tuple[ModelCallRequest, ...], definitions_by_id: dict[str, ToolDefinition]
    ) -> bool:
        if len(requests) < 2:
            return False
        for request in requests:
            definition = definitions_by_id.get(request.callable_id)
            if (
                definition is None
                or definition.impact is not ToolImpact.READ_ONLY
                or definition.requires_approval
                or definition.execution_mode is not ToolExecutionMode.INLINE
            ):
                return False
        return True

    async def _execute_tools(self, calls: list[tuple[ToolCall, bool, bool]]) -> list[ToolResult]:
        async def execute(call: ToolCall, allowed: bool, within_budget: bool) -> ToolResult:
            if not allowed:
                return self._tool_failure(
                    call, "tool_not_allowed", f"Tool {call.tool_id!r} is not allowed for this execution."
                )
            if not within_budget:
                return self._tool_failure(
                    call, "tool_budget_exhausted", "This task has used all of its tool calls. Return the final output."
                )
            return await self._tool_runner.execute(call)

        outcomes = await asyncio.gather(*(execute(*call) for call in calls), return_exceptions=True)
        for outcome in outcomes:
            if isinstance(outcome, BaseException):
                raise outcome
        return list(outcomes)

    @staticmethod
    def _tool_failure(call: ToolCall, code: str, message: str) -> ToolResult:
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_session_id=call.parent_session_id,
            status=ToolResultStatus.FAILED,
            error=ContractError(code=code, message=message),
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
            session_id=task.session_id,
            parent_session_id=task.parent_session_id,
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
