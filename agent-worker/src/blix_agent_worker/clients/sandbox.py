from typing import Annotated, Literal, Self

import httpx
from pydantic import ConfigDict, Field

from blix_agent_worker.models.common import ContractModel, NonEmptyText
from blix_agent_worker.models.task_input import (
    TaskAnswer as SandboxAnswer,
)
from blix_agent_worker.models.task_input import (
    TaskQuestion as SandboxQuestion,
)


class SandboxTraceEvent(ContractModel):
    sequence: Annotated[int, Field(ge=1)]
    timestamp: Annotated[str, Field(max_length=100)]
    kind: Annotated[str, Field(min_length=1, max_length=100)]
    content: Annotated[str, Field(max_length=8000)] = ""
    tool_name: Annotated[str, Field(max_length=200)] = ""
    tool_call_id: Annotated[str, Field(max_length=200)] = ""
    is_error: bool = False
    truncated: bool = False


class CodingInput(ContractModel):
    blix_run_url: Annotated[str, Field(pattern=r"^https?://[^\s<>]+$", max_length=2000)] | None = None
    requester_github_id: Annotated[int, Field(gt=0)] | None = None
    requester_name: Annotated[str, Field(min_length=1, max_length=200, pattern=r"^[^<>\r\n]+$")] | None = None
    requester_email: Annotated[str, Field(max_length=254, pattern=r"^[^\s<>@]+@[^\s<>@]+\.[^\s<>@]+$")] | None = None
    requester_github_login: (
        Annotated[str, Field(pattern=r"^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$", max_length=39)] | None
    ) = None
    context: Annotated[str, Field(max_length=16000)] = ""


class CodingOutput(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)

    summary: NonEmptyText
    base_sha: str = ""
    patch: str = ""
    changed_files: list[str] = Field(default_factory=list)
    pr_url: str | None = None
    commit_message: Annotated[str, Field(min_length=1, max_length=4000)] = "feat: implement coding task"


class SandboxTask(ContractModel):
    status: Literal["queued", "running", "waiting_for_input", "succeeded", "failed", "cancelled"]
    result: CodingOutput | None = None
    error: str | None = None
    question: SandboxQuestion | None = None
    trace: list[SandboxTraceEvent] = Field(default_factory=list)


class SandboxClient:
    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._client = httpx.AsyncClient(
            base_url=base_url,
            timeout=30,
            transport=transport,
            headers={"Authorization": "Bearer " + token},
        )

    async def __aenter__(self) -> Self:
        await self._client.__aenter__()
        return self

    async def __aexit__(self, *arguments) -> None:
        await self._client.__aexit__(*arguments)

    async def submit(self, task_id: str, objective: str, arguments: CodingInput) -> SandboxTask:
        response = await self._client.post(
            f"/tasks/{task_id}",
            json={**arguments.model_dump(mode="json"), "objective": objective},
        )
        response.raise_for_status()
        return SandboxTask.model_validate(response.json())

    async def get(self, task_id: str) -> SandboxTask:
        response = await self._client.get(f"/tasks/{task_id}")
        response.raise_for_status()
        return SandboxTask.model_validate(response.json())

    async def answer(self, task_id: str, answer: SandboxAnswer) -> None:
        response = await self._client.post(
            f"/tasks/{task_id}/answers",
            json=answer.model_dump(mode="json"),
        )
        response.raise_for_status()

    async def cancel(self, task_id: str) -> None:
        response = await self._client.delete(f"/tasks/{task_id}")
        if response.status_code != 404:
            response.raise_for_status()
