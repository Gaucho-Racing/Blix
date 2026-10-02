import asyncio
import os
from datetime import datetime
from typing import Self

import httpx
from pydantic import BaseModel, ConfigDict, Field

from blix_agent_worker.models.agent import AgentResult, AgentTask
from blix_agent_worker.models.common import DocumentID
from blix_agent_worker.models.context import CallerContext
from blix_agent_worker.models.message import RunMessage


class RunwayClientError(Exception):
    def __init__(self, message: str, *, status_code: int | None = None) -> None:
        self.status_code = status_code
        super().__init__(message)


class ResponseModel(BaseModel):
    model_config = ConfigDict(extra="ignore")


class Attempt(ResponseModel):
    attempt_id: str
    run_id: str
    session_id: str
    target_id: str
    turn_id: str | None = None
    task_id: str | None = None
    version: int
    fencing_token: str
    worker_id: str
    deadline_at: datetime
    runtime_kind: str


class Run(ResponseModel):
    run_id: str
    caller_context: CallerContext
    document_ids: tuple[DocumentID, ...] = ()


class Session(ResponseModel):
    session_id: str
    parent_session_id: str | None = None
    profile_id: str
    runtime_kind: str
    memory: dict[str, object] = Field(default_factory=dict)


class Profile(ResponseModel):
    profile_id: str
    tool_ids: tuple[str, ...]
    executor_ids: tuple[str, ...] = ()
    policy: dict[str, object]


class RuntimeCheckpoint(ResponseModel):
    runtime_kind: str
    schema_version: int
    payload: dict[str, object]


class ExecutionContext(ResponseModel):
    attempt: Attempt
    run: Run
    session: Session
    profile: Profile
    turn: dict[str, object] | None = None
    task: dict[str, object] | None = None
    checkpoint: RuntimeCheckpoint | None = None
    messages: tuple[RunMessage, ...]
    notifications: tuple[dict[str, object], ...] = ()
    resume_result: AgentResult | None = None
    commands: tuple[dict[str, object], ...] = ()
    allowed_tool_ids: tuple[str, ...]
    lease_renew_after_ms: int = Field(gt=0)


class WorkerRegistration(ResponseModel):
    worker_id: str
    heartbeat_interval_ms: int = Field(gt=0)


class ExecutionResult(ResponseModel):
    status: str
    result: AgentResult | None = None


class RunwayClient:
    def __init__(self, base_url: str, *, transport: httpx.AsyncBaseTransport | None = None) -> None:
        self._client = httpx.AsyncClient(
            base_url=f"{base_url.rstrip('/')}/",
            headers={"Authorization": f"Bearer {os.environ.get('BLIX_INTERNAL_TOKEN', '')}"},
            timeout=30,
            transport=transport,
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self._client.aclose()

    async def request(
        self, method: str, path: str, payload: object = None, *, params: dict[str, str] | None = None
    ) -> dict[str, object]:
        try:
            response = await self._client.request(method, path, json=payload, params=params)
        except httpx.RequestError as error:
            raise RunwayClientError("Runway is unavailable.") from error
        if response.is_error:
            try:
                message = str(response.json().get("error", "Runway request failed."))
            except (ValueError, AttributeError):
                message = f"Runway returned HTTP {response.status_code}."
            raise RunwayClientError(message, status_code=response.status_code)
        try:
            body = response.json()
        except ValueError as error:
            raise RunwayClientError("Runway returned invalid JSON.") from error
        if not isinstance(body, dict):
            raise RunwayClientError("Runway returned an invalid response.")
        return body

    async def claim_execution(self, attempt_id: str, worker_id: str) -> ExecutionContext:
        body = await self.request(
            "POST", f"internal/v1/attempts/{attempt_id}/claim", {"worker_id": worker_id, "runtime_kind": "api"}
        )
        return ExecutionContext.model_validate(body)

    async def register_worker(
        self, worker_id: str, concurrency: int, background_concurrency: int
    ) -> WorkerRegistration:
        body = await self.request(
            "POST",
            "internal/v1/workers/register",
            {"worker_id": worker_id, "concurrency": concurrency, "background_concurrency": background_concurrency},
        )
        return WorkerRegistration.model_validate(body)

    async def heartbeat_worker(self, worker_id: str) -> None:
        await self.request("POST", f"internal/v1/workers/{worker_id}/heartbeat", {})


class AttemptLease:
    def __init__(self, runway: RunwayClient, attempt: Attempt, *, resume_consumed: bool = False) -> None:
        self.runway = runway
        self.attempt = attempt
        self.resume_consumed = resume_consumed
        self._lock = asyncio.Lock()

    def ownership(self) -> dict[str, object]:
        return {
            "worker_id": self.attempt.worker_id,
            "fencing_token": self.attempt.fencing_token,
            "expected_version": self.attempt.version,
        }

    async def post(self, operation: str, payload: dict[str, object] | None = None) -> dict[str, object]:
        async with self._lock:
            body = await self.runway.request(
                "POST",
                f"internal/v1/attempts/{self.attempt.attempt_id}/{operation}",
                {**self.ownership(), **(payload or {})},
            )
            if "version" in body:
                self.attempt.version = int(body["version"])
            if "parent_version" in body:
                self.attempt.version = int(body["parent_version"])
            return body

    async def events(self, events: list[dict[str, object]]) -> None:
        await self.post("events", {"events": events})

    async def checkpoint(
        self,
        state: dict[str, object],
        *,
        events: list[dict[str, object]] | None = None,
        command_ids: list[str] | None = None,
        memory: dict[str, object] | None = None,
    ) -> None:
        payload: dict[str, object] = {
            "runtime_kind": "api",
            "schema_version": 1,
            "payload": state,
            "events": events or [],
            "command_ids": command_ids or [],
            "resume_consumed": self.resume_consumed,
        }
        if memory is not None:
            payload["memory"] = memory
        await self.post("checkpoints", payload)

    async def delegate(
        self, task: AgentTask, request_ref: str, *, background: bool, state: dict[str, object] | None = None
    ) -> dict[str, object]:
        payload: dict[str, object] = {
            "invocation": task.model_dump(mode="json"),
            "request_ref": request_ref,
            "background": background,
        }
        if state is not None:
            payload["checkpoint"] = {
                "runtime_kind": "api",
                "schema_version": 1,
                "payload": state,
                "resume_consumed": self.resume_consumed,
            }
        return await self.post("delegations", payload)
