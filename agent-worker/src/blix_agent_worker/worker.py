import asyncio
import logging
import os
from collections.abc import Awaitable, Callable
from dataclasses import dataclass

import aiomqtt
from pydantic import BaseModel, ConfigDict, ValidationError

from blix_agent_worker.agents import AgentRegistry, AgentRunner
from blix_agent_worker.agents.general_conversation import (
    GeneralConversationV1Agent,
    GeneralConversationV1Input,
    GeneralConversationV1Output,
)
from blix_agent_worker.clients import ExecutionContext, RunwayClient, RunwayClientError, TavilyClient
from blix_agent_worker.llm import BifrostResponsesModel
from blix_agent_worker.models.agent import AgentResult, AgentResultStatus, AgentTask
from blix_agent_worker.models.common import ContractError, new_ulid
from blix_agent_worker.models.llm import ModelResponse
from blix_agent_worker.models.message import ConversationMessage
from blix_agent_worker.models.tool import ToolCall, ToolResult
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.current_time import CurrentTimeV1Tool
from blix_agent_worker.tools.web_extract import WebExtractV1Tool
from blix_agent_worker.tools.web_search import WebSearchV1Tool

BIFROST_BASE_URL = "https://bifrost.gauchoracing.com"
MODEL = "openai/gpt-5.6-luna"

logger = logging.getLogger("blix_agent_worker")


class DispatchMessage(BaseModel):
    model_config = ConfigDict(extra="forbid")

    event_id: str
    run_id: str
    execution_id: str


@dataclass
class ExecutionState:
    version: int


class AgentWorker:
    def __init__(
        self,
        runway: RunwayClient,
        worker_id: str,
        model: BifrostResponsesModel,
        tavily: TavilyClient,
    ) -> None:
        self._runway = runway
        self._worker_id = worker_id
        self._model = model
        registry = ToolRegistry(
            [
                CurrentTimeV1Tool(),
                WebExtractV1Tool(tavily),
                WebSearchV1Tool(tavily),
            ]
        )
        self._tool_runner = ToolRunner(registry)

    async def handle(self, dispatch: DispatchMessage) -> None:
        try:
            execution = await self._runway.claim_execution(dispatch.execution_id, self._worker_id)
        except RunwayClientError as error:
            if error.status_code == 409:
                return
            raise

        state = ExecutionState(version=execution.version)

        async def checkpoint(call: ToolCall, result: ToolResult) -> None:
            state.version = await self._runway.checkpoint_tool(
                execution.execution_id,
                self._worker_id,
                state.version,
                call,
                result,
            )

        async def checkpoint_model(sequence: int, response: ModelResponse) -> None:
            state.version = await self._runway.checkpoint_model(
                execution.execution_id,
                self._worker_id,
                state.version,
                MODEL,
                execution.messages[-1].message_id,
                sequence,
                response,
            )

        try:
            result = await self._run_execution(execution, checkpoint, checkpoint_model)
            if result.status is AgentResultStatus.SUCCEEDED:
                output = GeneralConversationV1Output.model_validate(result.output)
                await self._runway.complete_execution(
                    execution.execution_id,
                    self._worker_id,
                    state.version,
                    output.message,
                    result.usage,
                )
                return
            error = result.error or ContractError(code="agent_execution_failed", message="The agent execution failed.")
            await self._runway.fail_execution(
                execution.execution_id,
                self._worker_id,
                state.version,
                error,
            )
        except Exception as error:
            logger.exception("execution %s failed", execution.execution_id)
            try:
                await self._runway.fail_execution(
                    execution.execution_id,
                    self._worker_id,
                    state.version,
                    ContractError(code="agent_worker_failed", message=str(error) or "The agent worker failed."),
                )
            except RunwayClientError:
                logger.exception("failed to persist failure for execution %s", execution.execution_id)

    async def _run_execution(
        self,
        execution: ExecutionContext,
        checkpoint: Callable[[ToolCall, ToolResult], Awaitable[None]],
        checkpoint_model: Callable[[int, ModelResponse], Awaitable[None]],
    ) -> AgentResult:
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
        agent_input = GeneralConversationV1Input(messages=messages)
        agent = GeneralConversationV1Agent(
            model=self._model,
            tool_runner=self._tool_runner,
            agent_runner=AgentRunner(AgentRegistry()),
            on_tool_result=checkpoint,
            on_model_response=checkpoint_model,
        )
        task = AgentTask(
            run_id=execution.run_id,
            execution_id=execution.execution_id,
            parent_execution_id=execution.parent_execution_id,
            agent_id=execution.agent_id,
            objective="Respond to the latest user message.",
            input=agent_input.model_dump(mode="json"),
            allowed_tool_ids=agent.definition.tool_ids,
            allowed_subagent_ids=(),
        )
        return await agent.run(task)


async def run() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    credentials = _bifrost_credentials()
    runway_url = os.environ.get("RUNWAY_URL", "http://localhost:10400").strip()
    mqtt_host = os.environ.get("MQTT_HOST", "localhost").strip()
    mqtt_port = int(os.environ.get("MQTT_PORT", "1883"))
    topic = os.environ.get(
        "MQTT_EXECUTION_READY_TOPIC",
        "$share/blix-agent-workers/blix/v1/executions/ready",
    ).strip()
    worker_id = f"agent-worker-{new_ulid()}"

    async with (
        RunwayClient(runway_url) as runway,
        BifrostResponsesModel(
            base_url=BIFROST_BASE_URL,
            username=credentials["BIFROST_USERNAME"],
            password=credentials["BIFROST_PASSWORD"],
            virtual_key=credentials["BIFROST_VIRTUAL_KEY"],
            model=MODEL,
        ) as model,
        TavilyClient() as tavily,
    ):
        worker = AgentWorker(runway, worker_id, model, tavily)
        client = aiomqtt.Client(
            mqtt_host,
            port=mqtt_port,
            identifier=worker_id,
        )
        while True:
            try:
                async with client:
                    await client.subscribe(topic, qos=1)
                    logger.info("worker %s subscribed to %s", worker_id, topic)
                    async for message in client.messages:
                        try:
                            dispatch = DispatchMessage.model_validate_json(message.payload)
                        except ValidationError:
                            logger.exception("discarding invalid dispatch message")
                            continue
                        await worker.handle(dispatch)
            except aiomqtt.MqttError as error:
                logger.warning("MQTT connection failed; reconnecting: %s", error)
                await asyncio.sleep(2)


def _bifrost_credentials() -> dict[str, str]:
    credentials = {
        "BIFROST_USERNAME": os.environ.get("BIFROST_USERNAME", "").strip(),
        "BIFROST_PASSWORD": os.environ.get("BIFROST_PASSWORD", ""),
        "BIFROST_VIRTUAL_KEY": os.environ.get("BIFROST_VIRTUAL_KEY", "").strip(),
    }
    missing = [name for name, value in credentials.items() if not value]
    if missing:
        raise RuntimeError(f"Missing required environment variables: {', '.join(missing)}")
    return credentials


def main() -> None:
    try:
        asyncio.run(run())
    except KeyboardInterrupt:
        pass
