import json
import re
from collections.abc import Awaitable, Callable
from typing import Annotated, Self

from pydantic import Field, HttpUrl, TypeAdapter, ValidationError, field_validator, model_validator

from blix_agent_worker.agents.callables import CallableCatalog, CallableNotFoundError
from blix_agent_worker.agents.deep_research.prompt import (
    DEEP_RESEARCH_V1_CONTEXT_SECTION,
    DEEP_RESEARCH_V1_SYSTEM_PROMPT,
    DEEP_RESEARCH_V1_USER_PROMPT,
)
from blix_agent_worker.llm import LanguageModel, ModelClientError
from blix_agent_worker.models.agent import (
    AgentDefinition,
    AgentExecutionPhase,
    AgentExecutionState,
    AgentKind,
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
    NonEmptyText,
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
from blix_agent_worker.models.tool import ToolCall, ToolResult, ToolResultStatus
from blix_agent_worker.tools import ToolRunner

_HTTP_URL_VALIDATOR = TypeAdapter(HttpUrl)


class DeepResearchV1Input(ContractModel):
    question: NonEmptyText


class DeepResearchSource(ContractModel):
    index: Annotated[int, Field(ge=1)]
    title: NonEmptyText
    url: NonEmptyText

    @field_validator("url")
    @classmethod
    def url_must_be_http(cls, value: str) -> str:
        _HTTP_URL_VALIDATOR.validate_python(value)
        return value


class DeepResearchV1Output(ContractModel):
    report: NonEmptyText
    sources: tuple[DeepResearchSource, ...]

    @model_validator(mode="after")
    def validate_source_index(self) -> Self:
        source_indices = [source.index for source in self.sources]
        if source_indices != list(range(1, len(self.sources) + 1)):
            raise ValueError("source indices must be sequential and start at 1")

        source_urls = [str(source.url) for source in self.sources]
        if len(source_urls) != len(set(source_urls)):
            raise ValueError("source URLs must be unique")

        citations = {int(value) for value in re.findall(r"\[(\d+)]", self.report)}
        unknown_citations = citations.difference(source_indices)
        if unknown_citations:
            raise ValueError("report contains citations that are missing from sources")
        unused_sources = set(source_indices).difference(citations)
        if unused_sources:
            raise ValueError("every indexed source must be cited in the report")
        return self


class DeepResearchV1Agent:
    definition = AgentDefinition(
        id="deep_research_v1",
        kind=AgentKind.SPECIALIST,
        description=(
            "Research a question using public web sources, inspect the strongest sources, and return a rigorous "
            "report with inline source URLs."
        ),
        instructions=DEEP_RESEARCH_V1_SYSTEM_PROMPT,
        input_schema=DeepResearchV1Input.model_json_schema(mode="validation"),
        output_schema=DeepResearchV1Output.model_json_schema(mode="serialization"),
        model_profile="deep_research",
        tool_ids=("current_time_v1", "web_extract_v1", "web_search_v1"),
    )

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
            agent_input = DeepResearchV1Input.model_validate(task.input)
        except ValidationError as error:
            return self._failure(
                task,
                "invalid_agent_input",
                "The deep research input is invalid.",
                details=validation_error_details(error),
            )

        tool_definitions = self._tool_runner.registry.definitions()
        allowed_tool_ids = frozenset(definition.id for definition in tool_definitions)
        catalog = CallableCatalog(tools=tool_definitions)

        if state is None:
            prompt = DEEP_RESEARCH_V1_USER_PROMPT.format(
                objective=task.objective,
                question=agent_input.question,
            )
            if task.context:
                prompt += DEEP_RESEARCH_V1_CONTEXT_SECTION.format(context=json.dumps(task.context, ensure_ascii=False))
            messages = [
                ModelMessage(role=ModelMessageRole.SYSTEM, content=self.definition.instructions),
                ModelMessage(role=ModelMessageRole.USER, content=prompt),
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

        if phase is AgentExecutionPhase.WAITING_FOR_AGENT or resume_result is not None:
            return self._failure(
                task,
                "invalid_execution_state",
                "The deep research agent cannot resume an agent delegation.",
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
                    output = DeepResearchV1Output.model_validate_json(response_message.content)
                except ValidationError as error:
                    return self._failure(
                        task,
                        "invalid_agent_output",
                        "The model returned an invalid deep research result.",
                        details=validation_error_details(error),
                        usage=usage,
                    )
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
                    "The research agent may only call tools.",
                    usage=usage,
                )

            usage = usage.model_copy(update={"tool_calls": usage.tool_calls + 1})
            call = ToolCall(
                run_id=task.run_id,
                call_id=new_ulid(),
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
