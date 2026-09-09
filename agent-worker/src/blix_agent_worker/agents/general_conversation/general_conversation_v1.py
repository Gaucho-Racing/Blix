import json
from collections.abc import Awaitable, Callable
from typing import Annotated, Self

from pydantic import Field, ValidationError, model_validator

from blix_agent_worker.agents.callables import CallableCatalog, CallableCatalogError, CallableNotFoundError
from blix_agent_worker.agents.general_conversation.prompt import GENERAL_CONVERSATION_V1_SYSTEM_PROMPT
from blix_agent_worker.agents.runner import AgentRunner
from blix_agent_worker.llm import LanguageModel, ModelClientError
from blix_agent_worker.models.agent import (
    AgentDefinition,
    AgentExecutionPhase,
    AgentExecutionState,
    AgentKind,
    AgentResult,
    AgentResultStatus,
    AgentRunOutcome,
    AgentSuspension,
    AgentTask,
    AgentUsage,
    PendingAgentCall,
)
from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    JsonObject,
    NonEmptyText,
    new_ulid,
    validation_error_details,
)
from blix_agent_worker.models.llm import (
    ModelCallableKind,
    ModelContinuation,
    ModelMessage,
    ModelMessageRole,
    ModelRequest,
    ModelResponse,
)
from blix_agent_worker.models.message import ConversationMessage, ConversationMessageRole
from blix_agent_worker.models.tool import ToolCall, ToolResult, ToolResultStatus
from blix_agent_worker.tools import ToolRunner


class GeneralConversationV1Input(ContractModel):
    messages: Annotated[tuple[ConversationMessage, ...], Field(min_length=1)]

    @model_validator(mode="after")
    def validate_messages(self) -> Self:
        if self.messages[-1].role is not ConversationMessageRole.USER:
            raise ValueError("the latest conversation message must be from the user")
        return self


class GeneralConversationV1Output(ContractModel):
    message: NonEmptyText


class GeneralConversationV1Agent:
    definition = AgentDefinition(
        id="general_conversation_v1",
        kind=AgentKind.ENTRYPOINT,
        description="Handle a general text conversation and use available tools when helpful.",
        instructions=GENERAL_CONVERSATION_V1_SYSTEM_PROMPT,
        input_schema=GeneralConversationV1Input.model_json_schema(mode="validation"),
        output_schema=GeneralConversationV1Output.model_json_schema(mode="serialization"),
        model_profile="general_reasoning",
        tool_ids=("current_time_v1", "web_extract_v1", "web_search_v1"),
        subagent_ids=("deep_research_v1",),
    )

    def __init__(
        self,
        *,
        model: LanguageModel,
        tool_runner: ToolRunner,
        agent_runner: AgentRunner,
        on_tool_result: Callable[[ToolCall, ToolResult, AgentExecutionState], Awaitable[None]] | None = None,
        on_model_response: Callable[[int, ModelResponse, AgentExecutionState], Awaitable[None]] | None = None,
    ) -> None:
        self._model = model
        self._tool_runner = tool_runner
        self._agent_runner = agent_runner
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
            agent_input = GeneralConversationV1Input.model_validate(task.input)
        except ValidationError as error:
            return self._failure(
                task,
                "invalid_agent_input",
                "The conversation agent input is invalid.",
                details=validation_error_details(error),
            )
        if any(message.run_id != task.run_id for message in agent_input.messages):
            return self._failure(task, "invalid_agent_input", "All messages must belong to the task run.")
        if any(message.parent_execution_id != task.execution_id for message in agent_input.messages):
            return self._failure(task, "invalid_agent_input", "All messages must belong to the agent execution.")

        tool_definitions = self._tool_runner.registry.definitions()
        allowed_tool_ids = frozenset(definition.id for definition in tool_definitions)
        subagent_definitions = self._agent_runner.registry.definitions()
        allowed_subagent_ids = frozenset(definition.id for definition in subagent_definitions)
        try:
            catalog = CallableCatalog(tools=tool_definitions, agents=subagent_definitions)
        except CallableCatalogError as error:
            return self._failure(task, "agent_configuration_error", str(error))

        if state is None:
            messages = [
                ModelMessage(role=ModelMessageRole.SYSTEM, content=self.definition.instructions),
                *(
                    ModelMessage(
                        role=(
                            ModelMessageRole.USER
                            if message.role is ConversationMessageRole.USER
                            else ModelMessageRole.ASSISTANT
                        ),
                        content=message.content,
                    )
                    for message in agent_input.messages
                ),
            ]
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

        if phase is AgentExecutionPhase.WAITING_FOR_AGENT:
            pending = state.pending_agent_call if state is not None else None
            if pending is None or pending.task.parent_execution_id != task.execution_id:
                return self._failure(task, "invalid_execution_state", "The pending agent call is invalid.", usage=usage)
            if resume_result is None:
                return self._failure(
                    task, "missing_agent_result", "The pending agent result is unavailable.", usage=usage
                )
            agent_result = self._agent_runner.validate_result(pending.task, resume_result)
            messages.append(
                ModelMessage(
                    role=ModelMessageRole.CALL_OUTPUT,
                    content=json.dumps(self._agent_result_payload(agent_result), separators=(",", ":")),
                    call_request_ref=pending.request_ref,
                )
            )
            call_batch = self._latest_call_batch(messages)
            next_call_index = pending.request_index + 1
            phase = (
                AgentExecutionPhase.PROCESSING_CALLS
                if call_batch is not None and next_call_index < len(call_batch.call_requests)
                else AgentExecutionPhase.READY_FOR_MODEL
            )
        elif resume_result is not None:
            return self._failure(
                task, "unexpected_agent_result", "The execution is not waiting for an agent.", usage=usage
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
                output = GeneralConversationV1Output(message=response_message.content)
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
                continuation = response.continuation
                messages.append(response.message)
                phase = (
                    AgentExecutionPhase.PROCESSING_CALLS
                    if response.message.call_requests
                    else AgentExecutionPhase.COMPLETED
                )
                next_call_index = 0
                checkpoint_state = self._state(
                    messages,
                    continuation,
                    usage,
                    phase,
                    next_call_index,
                )
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

            if callable_kind is ModelCallableKind.AGENT:
                usage = usage.model_copy(update={"delegation_calls": usage.delegation_calls + 1})
                prepared = self._agent_runner.prepare(
                    parent_task=task,
                    request=model_call_request,
                    allowed_agent_ids=allowed_subagent_ids,
                )
                if isinstance(prepared, AgentResult):
                    messages.append(
                        ModelMessage(
                            role=ModelMessageRole.CALL_OUTPUT,
                            content=json.dumps(self._agent_result_payload(prepared), separators=(",", ":")),
                            call_request_ref=model_call_request.ref,
                        )
                    )
                    next_call_index += 1
                    continue
                pending = PendingAgentCall(
                    request_ref=model_call_request.ref,
                    request_index=next_call_index,
                    task=prepared,
                )
                suspension_state = self._state(
                    messages,
                    continuation,
                    usage,
                    AgentExecutionPhase.WAITING_FOR_AGENT,
                    next_call_index,
                    pending,
                )
                return AgentSuspension(state=suspension_state, child_task=prepared)

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

    def _state(
        self,
        messages: list[ModelMessage],
        continuation: ModelContinuation | None,
        usage: AgentUsage,
        phase: AgentExecutionPhase,
        next_call_index: int,
        pending_agent_call: PendingAgentCall | None = None,
    ) -> AgentExecutionState:
        return AgentExecutionState(
            agent_id=self.definition.id,
            phase=phase,
            messages=tuple(messages),
            continuation=continuation,
            usage=usage,
            next_call_index=next_call_index,
            pending_agent_call=pending_agent_call,
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
    def _agent_result_payload(result: AgentResult) -> JsonObject:
        payload: JsonObject = {
            "status": result.status.value,
            "output": result.output,
            "artifact_refs": list(result.artifact_refs),
        }
        if result.error is not None:
            payload["error"] = result.error.model_dump(mode="json")
        return payload

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
