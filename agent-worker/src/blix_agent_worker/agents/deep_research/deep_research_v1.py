import json
from collections.abc import Callable

from pydantic import ValidationError

from blix_agent_worker.agents.callables import CallableCatalog, CallableNotFoundError
from blix_agent_worker.llm import LanguageModel, ModelClientError
from blix_agent_worker.models.agent import (
    AgentDefinition,
    AgentKind,
    AgentResult,
    AgentResultStatus,
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
    ModelRequest,
)
from blix_agent_worker.models.tool import ToolCall, ToolResult, ToolResultStatus
from blix_agent_worker.tools import ToolNotFoundError, ToolRunner


class DeepResearchV1Input(ContractModel):
    question: NonEmptyText


class DeepResearchV1Output(ContractModel):
    report: NonEmptyText


class DeepResearchV1Agent:
    definition = AgentDefinition(
        id="deep_research_v1",
        kind=AgentKind.SPECIALIST,
        description=(
            "Research a question using public web sources, inspect the strongest sources, and return a rigorous "
            "report with inline source URLs."
        ),
        instructions=(
            "You are Blix's deep research specialist. Research the assigned question using the available web tools. "
            "Search iteratively with focused queries, inspect the strongest relevant pages, and prioritize primary "
            "sources. Distinguish sourced facts from inference, note meaningful conflicts or uncertainty, and cite the "
            "supporting source URL next to each substantive claim. Never invent a source or claim to have inspected a "
            "page you did not extract. If the tools fail or evidence is insufficient, state that limitation clearly."
        ),
        input_schema=DeepResearchV1Input.model_json_schema(mode="validation"),
        output_schema=DeepResearchV1Output.model_json_schema(mode="serialization"),
        model_profile="deep_research",
        tool_ids=("web_search_v1", "web_extract_v1"),
    )

    def __init__(
        self,
        *,
        model: LanguageModel,
        tool_runner: ToolRunner,
        on_tool_result: Callable[[ToolCall, ToolResult], None] | None = None,
    ) -> None:
        self._model = model
        self._tool_runner = tool_runner
        self._on_tool_result = on_tool_result

    async def run(self, task: AgentTask) -> AgentResult:
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

        configured_tool_ids = frozenset(self.definition.tool_ids)
        allowed_tool_ids = configured_tool_ids if task.allowed_tool_ids is None else frozenset(task.allowed_tool_ids)
        disallowed_tool_ids = allowed_tool_ids.difference(configured_tool_ids)
        if disallowed_tool_ids:
            return self._failure(
                task,
                "invalid_tool_scope",
                "The task grants tools that are not configured for this agent.",
                details={"tool_ids": sorted(disallowed_tool_ids)},
            )
        if task.allowed_subagent_ids:
            return self._failure(
                task,
                "invalid_subagent_scope",
                "The deep research agent does not allow subagent delegation.",
                details={"agent_ids": sorted(task.allowed_subagent_ids)},
            )

        try:
            tool_definitions = self._tool_runner.registry.definitions(allowed_tool_ids)
        except ToolNotFoundError as error:
            return self._failure(
                task,
                "agent_configuration_error",
                str(error),
                details={"tool_id": error.tool_id},
            )

        catalog = CallableCatalog(tools=tool_definitions)
        prompt = f"Objective: {task.objective}\n\nResearch question: {agent_input.question}"
        if task.context:
            prompt += f"\n\nAdditional context:\n{json.dumps(task.context, ensure_ascii=False)}"
        messages = [
            ModelMessage(role=ModelMessageRole.SYSTEM, content=self.definition.instructions),
            ModelMessage(role=ModelMessageRole.USER, content=prompt),
        ]
        model_calls = 0
        tool_calls = 0
        input_tokens = 0
        output_tokens = 0
        continuation: ModelContinuation | None = None

        for _ in range(task.budget.max_model_calls):
            model_calls += 1
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
                    usage=self._usage(model_calls, tool_calls, input_tokens, output_tokens),
                )

            input_tokens += response.usage.input_tokens
            output_tokens += response.usage.output_tokens
            continuation = response.continuation
            usage = self._usage(model_calls, tool_calls, input_tokens, output_tokens)

            if not response.message.call_requests:
                if response.message.content is None:
                    return self._failure(
                        task,
                        "invalid_model_response",
                        "The model returned neither text nor a callable request.",
                        usage=usage,
                    )
                output = DeepResearchV1Output(report=response.message.content)
                return AgentResult(
                    run_id=task.run_id,
                    invocation_id=task.invocation_id,
                    agent_id=self.definition.id,
                    status=AgentResultStatus.SUCCEEDED,
                    output=output.model_dump(mode="json"),
                    usage=usage,
                )

            messages.append(response.message)
            for model_call_request in response.message.call_requests:
                if tool_calls >= task.budget.max_tool_calls:
                    return self._failure(
                        task,
                        "tool_budget_exhausted",
                        "The agent exhausted its tool-call budget.",
                        usage=usage,
                    )
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

                tool_calls += 1
                call = ToolCall(
                    call_id=new_ulid(),
                    tool_id=model_call_request.callable_id,
                    arguments=model_call_request.arguments,
                )
                if model_call_request.callable_id not in allowed_tool_ids:
                    tool_result = ToolResult(
                        call_id=call.call_id,
                        status=ToolResultStatus.FAILED,
                        error=ContractError(
                            code="tool_not_allowed",
                            message=f"Tool {call.tool_id!r} is not allowed for this invocation.",
                        ),
                    )
                else:
                    tool_result = await self._tool_runner.execute(call)

                if self._on_tool_result is not None:
                    self._on_tool_result(call, tool_result)
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
                        usage=self._usage(model_calls, tool_calls, input_tokens, output_tokens),
                    )
                messages.append(
                    ModelMessage(
                        role=ModelMessageRole.CALL_OUTPUT,
                        content=json.dumps(tool_result.model_dump(mode="json"), separators=(",", ":")),
                        call_request_ref=model_call_request.ref,
                    )
                )

        return self._failure(
            task,
            "model_budget_exhausted",
            "The agent exhausted its model-call budget before producing a report.",
            usage=self._usage(model_calls, tool_calls, input_tokens, output_tokens),
        )

    @staticmethod
    def _usage(model_calls: int, tool_calls: int, input_tokens: int, output_tokens: int) -> AgentUsage:
        return AgentUsage(
            model_calls=model_calls,
            tool_calls=tool_calls,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
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
            invocation_id=task.invocation_id,
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
