import asyncio
import logging
import os
from datetime import UTC, datetime

import httpx

from blix_agent_worker.clients.runway import AttemptLease, RunwayClientError
from blix_agent_worker.clients.sandbox import CodingInput, CodingOutput, SandboxAnswer, SandboxClient
from blix_agent_worker.models.agent import AgentDefinition, AgentKind, AgentResult, AgentResultStatus, AgentTask
from blix_agent_worker.models.common import ContractError

logger = logging.getLogger(__name__)


def delegation_schema() -> dict:
    schema = CodingInput.model_json_schema()
    schema["properties"] = {"context": schema["properties"]["context"]}
    return schema


class BlixCodeV1Agent:
    definition = AgentDefinition(
        id="blix_code_v1",
        kind=AgentKind.SPECIALIST,
        description=(
            "Implement a coding task as blix_code_v1 in a disposable Docker sandbox. "
            "Provide the task objective and relevant context in natural language. "
            "The agent discovers repositories and branches and chooses the authorized delivery workflow. "
            "The runtime supplies verified requester identity and renewable GitHub credentials. "
            "Can ask clarification questions and continue in the same live sandbox after an answer. "
            "Workspaces are ephemeral; live steering and session recovery are not supported in this MVP."
        ),
        instructions="Run the delegated coding task in the sandbox host.",
        input_schema=delegation_schema(),
        output_schema=CodingOutput.model_json_schema(),
    )

    def __init__(self, lease: AttemptLease) -> None:
        self.lease = lease

    async def run(self, task: AgentTask, *, state=None, resume_result=None) -> AgentResult:
        if state is not None or resume_result is not None:
            raise ValueError("Ephemeral coding tasks cannot resume checkpoints.")
        base_url = os.environ.get("SANDBOX_URL", "").strip()
        if not base_url:
            raise ValueError("SANDBOX_URL is not configured.")
        arguments = CodingInput.model_validate(task.input)
        task_id = self.lease.attempt.attempt_id
        async with SandboxClient(base_url, os.environ.get("BLIX_INTERNAL_TOKEN", "")) as client:
            try:
                await client.submit(task_id, task.objective, arguments)
                last_status = None
                notified_questions: set[str] = set()
                delivered_commands: set[str] = set()
                trace_cursor = 0
                while True:
                    payload = await client.get(task_id)
                    trace_events = []
                    for event in payload.trace:
                        if event.sequence <= trace_cursor:
                            continue
                        if event.sequence > trace_cursor + 1:
                            trace_events.append(
                                {
                                    "type": "runtime.coding_trace_v1",
                                    "entity_id": task.session_id,
                                    "payload": {
                                        "kind": "gap",
                                        "content": (
                                            f"{event.sequence - trace_cursor - 1} blix_code_v1 activity events "
                                            "exceeded the trace buffer."
                                        ),
                                    },
                                }
                            )
                        content = event.content or event.kind.replace("_", " ")
                        trace_events.append(
                            {
                                "type": "runtime.coding_trace_v1",
                                "entity_id": task.session_id,
                                "payload": {**event.model_dump(mode="json"), "content": content},
                            }
                        )
                        trace_cursor = event.sequence
                    for start in range(0, len(trace_events), 32):
                        await self.lease.events(trace_events[start : start + 32])
                    if delivered_commands:
                        await self.acknowledge_answers(delivered_commands)
                    status = payload.status
                    if status != last_status:
                        await self.lease.events(
                            [
                                {
                                    "type": "runtime.progress_v1",
                                    "entity_id": task.session_id,
                                    "payload": {"status": status},
                                }
                            ]
                        )
                        last_status = status
                    if status == "waiting_for_input":
                        question = payload.question
                        if question is None:
                            raise ValueError("Sandbox is waiting without a clarification question.")
                        if question.id not in notified_questions:
                            await self.lease.events(
                                [
                                    {
                                        "type": "runtime.question_v1",
                                        "entity_id": task.session_id,
                                        "payload": question.model_dump(mode="json"),
                                    }
                                ]
                            )
                            notified_questions.add(question.id)
                    if status == "succeeded" and not delivered_commands:
                        output = payload.result
                        if output is None:
                            raise ValueError("Sandbox succeeded without a result.")
                        return AgentResult(
                            run_id=task.run_id,
                            session_id=task.session_id,
                            parent_session_id=task.parent_session_id,
                            agent_id=task.agent_id,
                            status=AgentResultStatus.SUCCEEDED,
                            output=output.model_dump(mode="json"),
                        )
                    if status in {"failed", "cancelled"}:
                        return AgentResult(
                            run_id=task.run_id,
                            session_id=task.session_id,
                            parent_session_id=task.parent_session_id,
                            agent_id=task.agent_id,
                            status=AgentResultStatus.FAILED,
                            output=payload.result.model_dump(mode="json") if payload.result else None,
                            error=ContractError(
                                code="coding_failed",
                                message=payload.error or "Task cancelled.",
                            ),
                        )
                    commands = await self.lease.post("commands")
                    if any(command["kind"] == "cancel" for command in commands.get("commands", [])):
                        raise asyncio.CancelledError
                    for command in commands.get("commands", []):
                        if command["kind"] == "answer":
                            command_id = command["command_id"]
                            if command_id not in delivered_commands:
                                await client.answer(task_id, SandboxAnswer.model_validate(command["payload"]))
                                delivered_commands.add(command_id)
                            await self.acknowledge_answers(delivered_commands)
                    if datetime.now(UTC) >= self.lease.attempt.deadline_at:
                        raise TimeoutError("Coding task deadline exceeded.")
                    await asyncio.sleep(1)
            finally:
                try:
                    await asyncio.shield(client.cancel(task_id))
                except httpx.HTTPError:
                    logger.warning("Could not request sandbox cancellation for %s", task_id)

    async def acknowledge_answers(self, commands: set[str]) -> None:
        try:
            await self.lease.post("command-acks", {"command_ids": sorted(commands)})
        except RunwayClientError as error:
            if error.status_code not in {None, 429, 500, 502, 503, 504}:
                raise
            logger.warning("Sandbox accepted the answer; retrying Runway acknowledgment: %s", error)
        else:
            commands.clear()
