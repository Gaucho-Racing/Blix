import asyncio
import logging
import os
from collections.abc import Awaitable, Callable
from dataclasses import dataclass

import aiomqtt
from pydantic import BaseModel, ConfigDict, ValidationError

from blix_agent_worker.agents import AgentNotFoundError, AgentRegistry, AgentRunner
from blix_agent_worker.agents.deep_research import DeepResearchV1Agent
from blix_agent_worker.agents.general_conversation import (
    GeneralConversationV1Agent,
    GeneralConversationV1Input,
    GeneralConversationV1Output,
)
from blix_agent_worker.agents.knowledge_curator import KnowledgeCuratorV1Agent
from blix_agent_worker.clients import (
    ExecutionContext,
    RunwayClient,
    RunwayClientError,
    SentinelClient,
    SentinelClientError,
    SentinelUser,
    TavilyClient,
    WorkerRegistration,
)
from blix_agent_worker.clients.discord import DiscordClient
from blix_agent_worker.clients.knowledge import KnowledgeClient
from blix_agent_worker.llm import OpenAIResponsesModel
from blix_agent_worker.llm.retrying import RetryingModel
from blix_agent_worker.models.agent import (
    AgentExecutionState,
    AgentResult,
    AgentResultStatus,
    AgentRunOutcome,
    AgentSuspension,
    AgentTask,
)
from blix_agent_worker.models.common import ContractError, EventID, ExecutionID, RunID, new_ulid
from blix_agent_worker.models.context import EntityType
from blix_agent_worker.models.llm import ModelAttemptEvent, ModelResponse
from blix_agent_worker.models.message import ConversationMessage
from blix_agent_worker.models.tool import ToolCall, ToolResult
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.agent_execution import AgentExecutionV1Tool
from blix_agent_worker.tools.current_time import CurrentTimeV1Tool
from blix_agent_worker.tools.discord import DiscordReadV1Tool, DiscordWebhookSendV1Tool
from blix_agent_worker.tools.knowledge import (
    KnowledgeApplyV1Tool,
    KnowledgeEvidenceReadV1Tool,
    KnowledgeProposeV1Tool,
    KnowledgeReadV1Tool,
    KnowledgeRememberV1Tool,
    KnowledgeSourceCreateV1Tool,
    KnowledgeSourceRecordCreateV1Tool,
)
from blix_agent_worker.tools.sentinel_api import SentinelApiV1Tool
from blix_agent_worker.tools.sentinel_identity import SentinelIdentityResolveV1Tool
from blix_agent_worker.tools.sentinel_impersonate import SentinelImpersonateV1Tool
from blix_agent_worker.tools.web_extract import WebExtractV1Tool
from blix_agent_worker.tools.web_search import WebSearchV1Tool

logger = logging.getLogger("blix_agent_worker")

DEFAULT_AGENT_WORKER_CONCURRENCY = 4
DEFAULT_SENTINEL_URL = "https://sso.gauchoracing.com"
REQUIRED_BLIX_GROUP = "BlixUsers"


class CallerVerificationError(Exception):
    def __init__(self, code: str, message: str, *, retryable: bool = False) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class DispatchMessage(BaseModel):
    model_config = ConfigDict(extra="forbid")

    event_id: EventID
    run_id: RunID
    execution_id: ExecutionID
    parent_execution_id: ExecutionID | None = None


@dataclass
class ExecutionState:
    version: int


class AgentWorker:
    def __init__(
        self,
        runway: RunwayClient,
        worker_id: str,
        model: OpenAIResponsesModel,
        tavily: TavilyClient,
        sentinel: SentinelClient,
        knowledge: KnowledgeClient,
        discord: DiscordClient,
    ) -> None:
        self._runway = runway
        self._worker_id = worker_id
        self._model = model
        self._tavily = tavily
        self._sentinel = sentinel
        self._knowledge = knowledge
        self._discord = discord

    async def handle(self, dispatch: DispatchMessage) -> None:
        try:
            execution = await self._runway.claim_execution(dispatch.execution_id, self._worker_id)
        except RunwayClientError as error:
            if error.status_code == 409:
                return
            raise

        owner_task = asyncio.current_task()
        if owner_task is None:
            raise RuntimeError("execution handler has no owning task")
        relinquishing_lease = asyncio.Event()
        heartbeat_task = asyncio.create_task(
            self._maintain_lease(
                execution.execution_id,
                execution.lease_renew_after_ms,
                owner_task,
                relinquishing_lease,
            )
        )
        try:
            await self._handle_claimed(execution, relinquishing_lease)
        finally:
            heartbeat_task.cancel()
            await asyncio.gather(heartbeat_task, return_exceptions=True)

    async def _handle_claimed(self, execution: ExecutionContext, relinquishing_lease: asyncio.Event) -> None:
        state = ExecutionState(version=execution.version)
        turn_message_id = (
            execution.execution_id
            if execution.notifications
            else execution.messages[-1].message_id if execution.messages else execution.execution_id
        )

        async def record_model_attempt(event: ModelAttemptEvent) -> None:
            state.version = await self._runway.record_model_attempt(
                execution.execution_id,
                self._worker_id,
                state.version,
                turn_message_id,
                event,
            )

        async def checkpoint(call: ToolCall, result: ToolResult, execution_state: AgentExecutionState) -> None:
            state.version = await self._runway.checkpoint_tool(
                execution.execution_id,
                self._worker_id,
                state.version,
                call,
                result,
                execution_state,
            )

        async def checkpoint_model(
            sequence: int,
            response: ModelResponse,
            execution_state: AgentExecutionState,
        ) -> None:
            state.version = await self._runway.checkpoint_model(
                execution.execution_id,
                self._worker_id,
                state.version,
                self._model.model,
                turn_message_id,
                sequence,
                response,
                execution_state,
            )

        async def start_background_agent(task: AgentTask, request_ref: str) -> str:
            delegated = await self._runway.delegate_background_execution(
                execution.execution_id,
                self._worker_id,
                state.version,
                request_ref,
                task,
            )
            state.version = delegated.parent_version
            return delegated.execution_id

        try:
            caller_user = await self._resolve_caller(execution)
            state.version = await self._runway.checkpoint_caller(
                execution.execution_id,
                self._worker_id,
                state.version,
                caller_user.model_dump(mode="json"),
            )
            resume_result: AgentResult | None = None
            if execution.waiting_on_execution_id is not None:
                execution_result = await self._runway.get_execution_result(
                    execution.waiting_on_execution_id,
                    execution.execution_id,
                    self._worker_id,
                )
                if execution_result.result is None:
                    raise RuntimeError("resumed execution has no completed child result")
                resume_result = execution_result.result

            outcome = await self._run_execution(
                execution,
                caller_user,
                checkpoint,
                checkpoint_model,
                record_model_attempt,
                start_background_agent,
                resume_result,
            )
            if isinstance(outcome, AgentSuspension):
                relinquishing_lease.set()
                await self._runway.delegate_execution(
                    execution.execution_id,
                    self._worker_id,
                    state.version,
                    outcome.child_task,
                    outcome.state,
                )
                return
            result = outcome
            if execution.parent_execution_id is not None:
                relinquishing_lease.set()
                await self._runway.complete_agent_execution(
                    execution.execution_id,
                    self._worker_id,
                    state.version,
                    result,
                )
                return
            if result.status is AgentResultStatus.SUCCEEDED:
                output = GeneralConversationV1Output.model_validate(result.output)
                relinquishing_lease.set()
                await self._runway.complete_execution(
                    execution.execution_id,
                    self._worker_id,
                    state.version,
                    output.message,
                    result.usage,
                )
                return
            error = result.error or ContractError(code="agent_execution_failed", message="The agent execution failed.")
            relinquishing_lease.set()
            await self._runway.fail_execution(
                execution.execution_id,
                self._worker_id,
                state.version,
                error,
            )
        except CallerVerificationError as error:
            logger.warning("caller verification failed for execution %s: %s", execution.execution_id, error)
            try:
                relinquishing_lease.set()
                await self._runway.fail_execution(
                    execution.execution_id,
                    self._worker_id,
                    state.version,
                    ContractError(code=error.code, message=str(error), retryable=error.retryable),
                )
            except RunwayClientError:
                logger.exception("failed to persist caller verification failure for %s", execution.execution_id)
        except Exception as error:
            logger.exception("execution %s failed", execution.execution_id)
            try:
                relinquishing_lease.set()
                await self._runway.fail_execution(
                    execution.execution_id,
                    self._worker_id,
                    state.version,
                    ContractError(code="agent_worker_failed", message=str(error) or "The agent worker failed."),
                )
            except RunwayClientError:
                logger.exception("failed to persist failure for execution %s", execution.execution_id)

    async def _resolve_caller(self, execution: ExecutionContext) -> SentinelUser:
        sentinel_url = os.environ.get("SENTINEL_URL", "").strip() or DEFAULT_SENTINEL_URL
        service_account_token = os.environ.get("SENTINEL_SERVICE_ACCOUNT_TOKEN", "").strip()
        if not service_account_token:
            raise CallerVerificationError(
                "sentinel_not_configured",
                "SENTINEL_SERVICE_ACCOUNT_TOKEN is required to verify the caller.",
            )
        try:
            entity = await self._sentinel.get_entity(
                sentinel_url=sentinel_url,
                service_account_token=service_account_token,
                entity_id=execution.caller_context.entity_id,
            )
        except SentinelClientError as error:
            raise CallerVerificationError(error.code, str(error), retryable=error.retryable) from error

        caller = execution.caller_context
        if entity.id != caller.entity_id or entity.type is not caller.entity_type:
            raise CallerVerificationError(
                "caller_identity_mismatch",
                "The current Sentinel entity does not match the authenticated run caller.",
            )
        if caller.entity_type is not EntityType.USER or caller.user_id is None or entity.user is None:
            raise CallerVerificationError(
                "caller_not_user",
                "Only Sentinel user identities can start Blix agent executions.",
            )
        if entity.user.id != caller.user_id:
            raise CallerVerificationError(
                "caller_identity_mismatch",
                "The current Sentinel user does not match the authenticated run caller.",
            )
        if REQUIRED_BLIX_GROUP not in entity.user.groups:
            raise CallerVerificationError(
                "caller_not_blix_user",
                f"The Sentinel user must be an active member of {REQUIRED_BLIX_GROUP}.",
            )
        return entity.user

    async def _maintain_lease(
        self,
        execution_id: str,
        renew_after_ms: int,
        owner_task: asyncio.Task[None],
        relinquishing_lease: asyncio.Event,
    ) -> None:
        renew_after_seconds = renew_after_ms / 1000
        while True:
            await asyncio.sleep(renew_after_seconds)
            try:
                await self._runway.heartbeat_execution(execution_id, self._worker_id)
            except RunwayClientError as error:
                if error.status_code in {404, 409}:
                    if not relinquishing_lease.is_set():
                        logger.warning("execution %s lost its lease", execution_id)
                        owner_task.cancel()
                    return
                logger.warning("failed to renew lease for execution %s: %s", execution_id, error)
            except Exception:
                logger.exception("invalid lease heartbeat response for execution %s", execution_id)
                owner_task.cancel()
                return

    async def _run_execution(
        self,
        execution: ExecutionContext,
        caller_user: SentinelUser,
        checkpoint: Callable[[ToolCall, ToolResult, AgentExecutionState], Awaitable[None]],
        checkpoint_model: Callable[[int, ModelResponse, AgentExecutionState], Awaitable[None]],
        record_model_attempt: Callable[[ModelAttemptEvent], Awaitable[None]],
        start_background_agent: Callable[[AgentTask, str], Awaitable[str]],
        resume_result: AgentResult | None,
    ) -> AgentRunOutcome:
        model = RetryingModel(self._model, self._model.model, record_model_attempt)
        messages = tuple(
            ConversationMessage(
                message_id=message.message_id,
                run_id=message.run_id,
                parent_execution_id=message.parent_execution_id,
                role=message.role,
                content=message.content,
                created_at=message.created_at,
            )
            for message in execution.messages
        )
        tool_runner = ToolRunner(
            ToolRegistry(
                [
                    CurrentTimeV1Tool(),
                    AgentExecutionV1Tool(self._runway, self._worker_id),
                    DiscordReadV1Tool(self._discord, self._sentinel),
                    DiscordWebhookSendV1Tool(self._discord, caller_user),
                    KnowledgeReadV1Tool(self._knowledge),
                    KnowledgeEvidenceReadV1Tool(self._knowledge),
                    KnowledgeSourceCreateV1Tool(self._knowledge),
                    KnowledgeSourceRecordCreateV1Tool(self._knowledge, self._sentinel),
                    KnowledgeProposeV1Tool(self._knowledge),
                    KnowledgeApplyV1Tool(self._knowledge),
                    KnowledgeRememberV1Tool(
                        self._knowledge,
                        messages if execution.parent_execution_id is None else (),
                        execution.caller_context,
                    ),
                    SentinelApiV1Tool(self._sentinel, caller_user.id),
                    SentinelIdentityResolveV1Tool(self._sentinel),
                    SentinelImpersonateV1Tool(self._sentinel, caller_user.id),
                    WebExtractV1Tool(self._tavily),
                    WebSearchV1Tool(self._tavily),
                ]
            )
        )
        deep_research = DeepResearchV1Agent(
            model=model,
            tool_runner=tool_runner,
            on_tool_result=checkpoint,
            on_model_response=checkpoint_model,
        )
        knowledge_curator = KnowledgeCuratorV1Agent(
            model=model,
            tool_runner=tool_runner,
            on_tool_result=checkpoint,
            on_model_response=checkpoint_model,
        )
        agent_registry = AgentRegistry([deep_research, knowledge_curator])
        trusted_context = {
            "caller": {
                **execution.caller_context.model_dump(mode="json"),
                "user": caller_user.model_dump(mode="json"),
            }
        }
        if execution.parent_execution_id is not None:
            if execution.task is None:
                return self._agent_failure(execution, "missing_agent_task", "The delegated execution has no task.")
            try:
                agent = agent_registry.resolve(execution.agent_id)
            except AgentNotFoundError:
                return self._agent_failure(
                    execution,
                    "agent_not_registered",
                    f"Agent {execution.agent_id!r} is not registered on this worker.",
                )
            task_context = dict(execution.task.context)
            task_context.update(trusted_context)
            task = execution.task.model_copy(update={"context": task_context})
            return await agent.run(task, state=execution.state, resume_result=resume_result)

        agent_input = GeneralConversationV1Input(
            messages=messages,
            notifications=tuple(notification.model_dump(mode="json") for notification in execution.notifications),
        )
        agent = GeneralConversationV1Agent(
            model=model,
            tool_runner=tool_runner,
            agent_runner=AgentRunner(agent_registry),
            on_tool_result=checkpoint,
            on_model_response=checkpoint_model,
            on_background_agent=start_background_agent,
        )
        task = AgentTask(
            run_id=execution.run_id,
            execution_id=execution.execution_id,
            parent_execution_id=execution.parent_execution_id,
            agent_id=execution.agent_id,
            objective="Respond to the latest user message.",
            input=agent_input.model_dump(mode="json"),
            context=trusted_context,
        )
        return await agent.run(task, state=execution.state, resume_result=resume_result)

    @staticmethod
    def _agent_failure(execution: ExecutionContext, code: str, message: str) -> AgentResult:
        return AgentResult(
            run_id=execution.run_id,
            execution_id=execution.execution_id,
            parent_execution_id=execution.parent_execution_id,
            agent_id=execution.agent_id,
            status=AgentResultStatus.FAILED,
            error=ContractError(code=code, message=message),
        )


async def run() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    concurrency = _agent_worker_concurrency()
    runway_url = os.environ.get("RUNWAY_URL", "http://localhost:10400").strip()
    mqtt_host = os.environ.get("MQTT_HOST", "localhost").strip()
    mqtt_port = int(os.environ.get("MQTT_PORT", "1883"))
    topic = os.environ.get(
        "MQTT_EXECUTION_READY_TOPIC",
        "$share/blix-agent-workers/blix/v1/executions/ready",
    ).strip()
    worker_id = new_ulid("worker")

    async with (
        RunwayClient(runway_url) as runway,
        OpenAIResponsesModel.from_environment() as model,
        TavilyClient() as tavily,
        SentinelClient() as sentinel,
        KnowledgeClient() as knowledge,
        DiscordClient() as discord,
    ):
        registration = await _register_worker(runway, worker_id, concurrency)
        worker_heartbeat = asyncio.create_task(
            _maintain_worker_registration(
                runway,
                worker_id,
                concurrency,
                registration.heartbeat_interval_ms,
            )
        )
        worker = AgentWorker(runway, worker_id, model, tavily, sentinel, knowledge, discord)
        execution_capacity = asyncio.Semaphore(concurrency)
        active_executions: set[asyncio.Task[None]] = set()
        pending_execution_ids: set[str] = set()

        async def execute(dispatch: DispatchMessage) -> None:
            try:
                async with execution_capacity:
                    await worker.handle(dispatch)
            except Exception:
                logger.exception("dispatch for execution %s failed", dispatch.execution_id)
            finally:
                pending_execution_ids.discard(dispatch.execution_id)

        client = aiomqtt.Client(
            mqtt_host,
            port=mqtt_port,
            identifier=worker_id,
        )
        try:
            while True:
                try:
                    async with client:
                        await client.subscribe(topic, qos=1)
                        logger.info(
                            "worker %s subscribed to %s with concurrency=%d",
                            worker_id,
                            topic,
                            concurrency,
                        )
                        async for message in client.messages:
                            try:
                                dispatch = DispatchMessage.model_validate_json(message.payload)
                            except ValidationError:
                                logger.exception("discarding invalid dispatch message")
                                continue
                            if dispatch.execution_id in pending_execution_ids:
                                continue
                            pending_execution_ids.add(dispatch.execution_id)
                            task = asyncio.create_task(execute(dispatch))
                            active_executions.add(task)
                            task.add_done_callback(active_executions.discard)
                except aiomqtt.MqttError as error:
                    logger.warning("MQTT connection failed; reconnecting: %s", error)
                    await asyncio.sleep(2)
        finally:
            worker_heartbeat.cancel()
            await asyncio.gather(worker_heartbeat, return_exceptions=True)


async def _register_worker(runway: RunwayClient, worker_id: str, concurrency: int) -> WorkerRegistration:
    while True:
        try:
            registration = await runway.register_worker(worker_id, concurrency)
            logger.info("registered worker %s with concurrency=%d", worker_id, concurrency)
            return registration
        except RunwayClientError as error:
            logger.warning("worker registration failed; retrying: %s", error)
            await asyncio.sleep(2)


async def _maintain_worker_registration(
    runway: RunwayClient,
    worker_id: str,
    concurrency: int,
    heartbeat_interval_ms: int,
) -> None:
    heartbeat_interval_seconds = heartbeat_interval_ms / 1000
    while True:
        await asyncio.sleep(heartbeat_interval_seconds)
        try:
            await runway.heartbeat_worker(worker_id)
        except RunwayClientError as error:
            if error.status_code == 404:
                registration = await _register_worker(runway, worker_id, concurrency)
                heartbeat_interval_seconds = registration.heartbeat_interval_ms / 1000
                continue
            logger.warning("worker heartbeat failed: %s", error)


def _agent_worker_concurrency() -> int:
    raw_value = os.environ.get("AGENT_WORKER_CONCURRENCY", str(DEFAULT_AGENT_WORKER_CONCURRENCY)).strip()
    try:
        concurrency = int(raw_value)
    except ValueError as error:
        raise RuntimeError("AGENT_WORKER_CONCURRENCY must be an integer.") from error
    if concurrency < 1:
        raise RuntimeError("AGENT_WORKER_CONCURRENCY must be at least 1.")
    return concurrency


def main() -> None:
    try:
        asyncio.run(run())
    except KeyboardInterrupt:
        pass
