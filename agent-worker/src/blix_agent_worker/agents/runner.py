from collections.abc import Callable, Collection

from pydantic import ValidationError

from blix_agent_worker.agents.registry import AgentNotFoundError, AgentRegistry
from blix_agent_worker.models.agent import AgentResult, AgentResultStatus, AgentTask
from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    JsonObject,
    NonEmptyText,
    new_ulid,
    validation_error_details,
)
from blix_agent_worker.models.llm import ModelCallRequest


class AgentDelegationInput(ContractModel):
    objective: NonEmptyText
    input: JsonObject
    background: bool = False


class AgentRunner:
    def __init__(
        self,
        registry: AgentRegistry,
        *,
        on_start: Callable[[AgentTask], None] | None = None,
        on_result: Callable[[AgentResult], None] | None = None,
    ) -> None:
        self.registry = registry
        self._on_start = on_start
        self._on_result = on_result

    def prepare(
        self,
        *,
        parent_task: AgentTask,
        request: ModelCallRequest,
        allowed_agent_ids: Collection[str],
    ) -> tuple[AgentTask, bool] | AgentResult:
        session_id = new_ulid("session")
        if request.callable_id not in allowed_agent_ids:
            return self._emit(
                self._failure(
                    parent_task,
                    session_id,
                    request.callable_id,
                    "agent_not_allowed",
                    f"Agent {request.callable_id!r} is not allowed for this execution.",
                )
            )
        try:
            delegation = AgentDelegationInput.model_validate(request.arguments)
        except ValidationError as error:
            return self._emit(
                self._failure(
                    parent_task,
                    session_id,
                    request.callable_id,
                    "invalid_delegation_input",
                    "The delegated agent input is invalid.",
                    details=validation_error_details(error),
                )
            )

        try:
            agent = self.registry.resolve(request.callable_id)
        except AgentNotFoundError:
            return self._emit(
                self._failure(
                    parent_task,
                    session_id,
                    request.callable_id,
                    "agent_not_found",
                    f"Agent {request.callable_id!r} is not registered.",
                )
            )

        task = AgentTask(
            conversation_id=parent_task.conversation_id,
            session_id=session_id,
            parent_session_id=parent_task.session_id,
            agent_id=agent.definition.id,
            objective=delegation.objective,
            input=delegation.input,
            context=parent_task.context,
            artifact_refs=parent_task.artifact_refs,
            allowed_tool_ids=agent.definition.tool_ids,
            allowed_subagent_ids=agent.definition.subagent_ids,
            delegation_depth=parent_task.delegation_depth + 1,
            response_schema=agent.definition.output_schema,
        )
        if self._on_start is not None:
            self._on_start(task)
        return task, delegation.background

    def validate_result(self, task: AgentTask, result: AgentResult) -> AgentResult:
        if (
            result.conversation_id != task.conversation_id
            or result.session_id != task.session_id
            or result.parent_session_id != task.parent_session_id
            or result.agent_id != task.agent_id
        ):
            return self._emit(
                AgentResult(
                    conversation_id=task.conversation_id,
                    session_id=task.session_id,
                    parent_session_id=task.parent_session_id,
                    agent_id=task.agent_id,
                    status=AgentResultStatus.FAILED,
                    error=ContractError(
                        code="invalid_agent_result",
                        message=f"Agent {task.agent_id!r} returned mismatched execution metadata.",
                    ),
                )
            )
        return self._emit(result)

    def _emit(self, result: AgentResult) -> AgentResult:
        if self._on_result is not None:
            self._on_result(result)
        return result

    @staticmethod
    def _failure(
        parent_task: AgentTask,
        session_id: str,
        agent_id: str,
        code: str,
        message: str,
        *,
        details: JsonObject | None = None,
    ) -> AgentResult:
        return AgentResult(
            conversation_id=parent_task.conversation_id,
            session_id=session_id,
            parent_session_id=parent_task.session_id,
            agent_id=agent_id,
            status=AgentResultStatus.FAILED,
            error=ContractError(
                code=code,
                message=message,
                details=details or {},
            ),
        )
