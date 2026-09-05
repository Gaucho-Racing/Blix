from collections.abc import Awaitable, Callable, Collection

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


class AgentRunner:
    def __init__(
        self,
        registry: AgentRegistry,
        *,
        executor: Callable[[AgentTask], Awaitable[AgentResult]] | None = None,
        on_start: Callable[[AgentTask], None] | None = None,
        on_result: Callable[[AgentResult], None] | None = None,
    ) -> None:
        self.registry = registry
        self._executor = executor
        self._on_start = on_start
        self._on_result = on_result

    async def execute(
        self,
        *,
        parent_task: AgentTask,
        request: ModelCallRequest,
        allowed_agent_ids: Collection[str],
    ) -> AgentResult:
        execution_id = new_ulid()
        if request.callable_id not in allowed_agent_ids:
            return self._emit(
                self._failure(
                    parent_task,
                    execution_id,
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
                    execution_id,
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
                    execution_id,
                    request.callable_id,
                    "agent_not_found",
                    f"Agent {request.callable_id!r} is not registered.",
                )
            )

        task = AgentTask(
            run_id=parent_task.run_id,
            execution_id=execution_id,
            parent_execution_id=parent_task.execution_id,
            agent_id=agent.definition.id,
            objective=delegation.objective,
            input=delegation.input,
            context=parent_task.context,
            artifact_refs=parent_task.artifact_refs,
            allowed_tool_ids=None,
            allowed_subagent_ids=None,
            delegation_depth=parent_task.delegation_depth + 1,
            response_schema=agent.definition.output_schema,
        )
        if self._on_start is not None:
            self._on_start(task)

        try:
            result = await self._executor(task) if self._executor is not None else await agent.run(task)
        except Exception:
            return self._emit(
                self._failure(
                    parent_task,
                    execution_id,
                    agent.definition.id,
                    "agent_execution_failed",
                    f"Agent {agent.definition.id!r} failed unexpectedly.",
                )
            )

        if (
            result.run_id != task.run_id
            or result.execution_id != task.execution_id
            or result.parent_execution_id != task.parent_execution_id
            or result.agent_id != task.agent_id
        ):
            return self._emit(
                self._failure(
                    parent_task,
                    execution_id,
                    agent.definition.id,
                    "invalid_agent_result",
                    f"Agent {agent.definition.id!r} returned mismatched execution metadata.",
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
        execution_id: str,
        agent_id: str,
        code: str,
        message: str,
        *,
        details: JsonObject | None = None,
    ) -> AgentResult:
        return AgentResult(
            run_id=parent_task.run_id,
            execution_id=execution_id,
            parent_execution_id=parent_task.execution_id,
            agent_id=agent_id,
            status=AgentResultStatus.FAILED,
            error=ContractError(
                code=code,
                message=message,
                details=details or {},
            ),
        )
