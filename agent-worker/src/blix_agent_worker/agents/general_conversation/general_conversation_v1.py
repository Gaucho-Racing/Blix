import json
from collections.abc import Callable
from typing import Annotated, Self

from pydantic import Field, JsonValue, ValidationError, model_validator

from blix_agent_worker.agents.callables import CallableCatalog, CallableCatalogError, CallableNotFoundError
from blix_agent_worker.agents.general_conversation.prompt import GENERAL_CONVERSATION_V1_SYSTEM_PROMPT
from blix_agent_worker.agents.registry import AgentNotFoundError
from blix_agent_worker.agents.runner import AgentRunner
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
    ULID,
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
from blix_agent_worker.models.message import ConversationMessage, ConversationMessageRole
from blix_agent_worker.models.tool import ToolCall, ToolResult, ToolResultStatus
from blix_agent_worker.tools import ToolNotFoundError, ToolRunner


class GeneralConversationV1Input(ContractModel):
    conversation_id: ULID
    messages: Annotated[tuple[ConversationMessage, ...], Field(min_length=1)]

    @model_validator(mode="after")
    def validate_messages(self) -> Self:
        if any(message.conversation_id != self.conversation_id for message in self.messages):
            raise ValueError("all messages must belong to the input conversation")
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
        tool_ids=("current_time_v1",),
        subagent_ids=("deep_research_v1",),
    )

    def __init__(
        self,
        *,
        model: LanguageModel,
        tool_runner: ToolRunner,
        agent_runner: AgentRunner,
        on_tool_result: Callable[[ToolCall, ToolResult], None] | None = None,
    ) -> None:
        self._model = model
        self._tool_runner = tool_runner
        self._agent_runner = agent_runner
        self._on_tool_result = on_tool_result

    async def run(self, task: AgentTask) -> AgentResult:
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

        configured_tool_ids = frozenset(self.definition.tool_ids)
        allowed_tool_ids = configured_tool_ids if task.allowed_tool_ids is None else frozenset(task.allowed_tool_ids)
        disallowed_tool_ids = allowed_tool_ids.difference(configured_tool_ids)
        if disallowed_tool_ids:
            disallowed_values: list[JsonValue] = [tool_id for tool_id in sorted(disallowed_tool_ids)]
            return self._failure(
                task,
                "invalid_tool_scope",
                "The task grants tools that are not configured for this agent.",
                details={"tool_ids": disallowed_values},
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

        configured_subagent_ids = frozenset(self.definition.subagent_ids)
        allowed_subagent_ids = (
            configured_subagent_ids if task.allowed_subagent_ids is None else frozenset(task.allowed_subagent_ids)
        )
        disallowed_subagent_ids = allowed_subagent_ids.difference(configured_subagent_ids)
        if disallowed_subagent_ids:
            disallowed_values = [agent_id for agent_id in sorted(disallowed_subagent_ids)]
            return self._failure(
                task,
                "invalid_subagent_scope",
                "The task grants subagents that are not configured for this agent.",
                details={"agent_ids": disallowed_values},
            )
        try:
            subagent_definitions = self._agent_runner.registry.definitions(allowed_subagent_ids)
            catalog = CallableCatalog(tools=tool_definitions, agents=subagent_definitions)
        except AgentNotFoundError as error:
            return self._failure(
                task,
                "agent_configuration_error",
                str(error),
                details={"agent_id": error.agent_id},
            )
        except CallableCatalogError as error:
            return self._failure(task, "agent_configuration_error", str(error))

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
        model_calls = 0
        tool_calls = 0
        delegation_calls = 0
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
                    usage=AgentUsage(
                        model_calls=model_calls,
                        tool_calls=tool_calls,
                        delegation_calls=delegation_calls,
                        input_tokens=input_tokens,
                        output_tokens=output_tokens,
                    ),
                )

            input_tokens += response.usage.input_tokens
            output_tokens += response.usage.output_tokens
            continuation = response.continuation
            usage = AgentUsage(
                model_calls=model_calls,
                tool_calls=tool_calls,
                delegation_calls=delegation_calls,
                input_tokens=input_tokens,
                output_tokens=output_tokens,
            )

            if not response.message.call_requests:
                if response.message.content is None:
                    return self._failure(
                        task,
                        "invalid_model_response",
                        "The model returned neither text nor a callable request.",
                        usage=usage,
                    )
                output = GeneralConversationV1Output(message=response.message.content)
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
                    if delegation_calls >= task.budget.max_delegation_calls:
                        return self._failure(
                            task,
                            "delegation_budget_exhausted",
                            "The agent exhausted its delegation-call budget.",
                            usage=usage,
                        )
                    delegation_calls += 1
                    agent_result = await self._agent_runner.execute(
                        parent_task=task,
                        request=model_call_request,
                        allowed_agent_ids=allowed_subagent_ids,
                    )
                    messages.append(
                        ModelMessage(
                            role=ModelMessageRole.CALL_OUTPUT,
                            content=json.dumps(self._agent_result_payload(agent_result), separators=(",", ":")),
                            call_request_ref=model_call_request.ref,
                        )
                    )
                    usage = AgentUsage(
                        model_calls=model_calls,
                        tool_calls=tool_calls,
                        delegation_calls=delegation_calls,
                        input_tokens=input_tokens,
                        output_tokens=output_tokens,
                    )
                    continue

                if tool_calls >= task.budget.max_tool_calls:
                    return self._failure(
                        task,
                        "tool_budget_exhausted",
                        "The agent exhausted its tool-call budget.",
                        usage=AgentUsage(
                            model_calls=model_calls,
                            tool_calls=tool_calls,
                            delegation_calls=delegation_calls,
                            input_tokens=input_tokens,
                            output_tokens=output_tokens,
                        ),
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
            "The agent exhausted its model-call budget before producing a response.",
            usage=AgentUsage(
                model_calls=model_calls,
                tool_calls=tool_calls,
                delegation_calls=delegation_calls,
                input_tokens=input_tokens,
                output_tokens=output_tokens,
            ),
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
