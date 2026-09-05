from datetime import datetime
from typing import Self

import httpx

from blix_agent_worker.models.agent import AgentUsage
from blix_agent_worker.models.common import ULID, ContractError, ContractModel
from blix_agent_worker.models.llm import ModelResponse
from blix_agent_worker.models.message import ConversationMessageRole
from blix_agent_worker.models.tool import ToolCall, ToolResult


class RunwayClientError(Exception):
    def __init__(self, message: str, *, status_code: int | None = None) -> None:
        self.status_code = status_code
        super().__init__(message)


class RunwayMessage(ContractModel):
    message_id: ULID
    run_id: ULID
    parent_execution_id: ULID
    event_position: int
    role: ConversationMessageRole
    content: str
    created_at: datetime


class ExecutionContext(ContractModel):
    run_id: ULID
    execution_id: ULID
    parent_execution_id: ULID | None = None
    agent_id: str
    version: int
    messages: tuple[RunwayMessage, ...]


class VersionResponse(ContractModel):
    version: int


class RunwayClient:
    def __init__(self, base_url: str) -> None:
        if not base_url.strip():
            raise ValueError("base_url cannot be empty")
        self._client = httpx.AsyncClient(
            base_url=f"{base_url.rstrip('/')}/",
            headers={"Accept": "application/json", "Content-Type": "application/json"},
            timeout=30,
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._client.aclose()

    async def claim_execution(self, execution_id: str, worker_id: str) -> ExecutionContext:
        body = await self._post(
            f"internal/v1/executions/{execution_id}/claim",
            {"worker_id": worker_id},
        )
        return ExecutionContext.model_validate(body)

    async def checkpoint_tool(
        self,
        execution_id: str,
        worker_id: str,
        expected_version: int,
        call: ToolCall,
        result: ToolResult,
    ) -> int:
        body = await self._post(
            f"internal/v1/executions/{execution_id}/tool-checkpoints",
            {
                "worker_id": worker_id,
                "expected_version": expected_version,
                "call": call.model_dump(mode="json"),
                "result": result.model_dump(mode="json"),
            },
        )
        return VersionResponse.model_validate(body).version

    async def checkpoint_model(
        self,
        execution_id: str,
        worker_id: str,
        expected_version: int,
        model: str,
        turn_message_id: str,
        sequence: int,
        response: ModelResponse,
    ) -> int:
        body = await self._post(
            f"internal/v1/executions/{execution_id}/model-checkpoints",
            {
                "worker_id": worker_id,
                "expected_version": expected_version,
                "model": model,
                "turn_message_id": turn_message_id,
                "sequence": sequence,
                "input_tokens": response.usage.input_tokens,
                "output_tokens": response.usage.output_tokens,
                "callable_ids": [request.callable_id for request in response.message.call_requests],
                "has_content": response.message.content is not None,
            },
        )
        return VersionResponse.model_validate(body).version

    async def complete_execution(
        self,
        execution_id: str,
        worker_id: str,
        expected_version: int,
        content: str,
        usage: AgentUsage,
    ) -> None:
        await self._post(
            f"internal/v1/executions/{execution_id}/complete",
            {
                "worker_id": worker_id,
                "expected_version": expected_version,
                "content": content,
                "usage": usage.model_dump(mode="json"),
            },
        )

    async def fail_execution(
        self,
        execution_id: str,
        worker_id: str,
        expected_version: int,
        error: ContractError,
    ) -> None:
        await self._post(
            f"internal/v1/executions/{execution_id}/fail",
            {
                "worker_id": worker_id,
                "expected_version": expected_version,
                "error": error.model_dump(mode="json"),
            },
        )

    async def _post(self, path: str, payload: object) -> object:
        try:
            response = await self._client.post(path, json=payload)
        except httpx.RequestError as error:
            raise RunwayClientError("Runway is unavailable.") from error
        if response.is_error:
            raise RunwayClientError(self._error_message(response), status_code=response.status_code)
        try:
            return response.json()
        except ValueError as error:
            raise RunwayClientError("Runway returned invalid JSON.", status_code=response.status_code) from error

    @staticmethod
    def _error_message(response: httpx.Response) -> str:
        fallback = f"Runway returned HTTP {response.status_code}."
        try:
            body = response.json()
        except ValueError:
            return fallback
        if isinstance(body, dict) and isinstance(body.get("error"), str):
            return body["error"]
        return fallback
