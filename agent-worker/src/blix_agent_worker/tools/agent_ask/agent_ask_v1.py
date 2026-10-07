import asyncio
import hashlib
from collections.abc import Callable
from typing import Annotated

from pydantic import Field, ValidationError, model_validator

from blix_agent_worker.clients.runway import AttemptLease
from blix_agent_worker.models.common import ContractModel
from blix_agent_worker.models.task_input import QuestionItem, TaskAnswer, TaskInputRequiredError, TaskQuestion
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact, ToolResult, ToolResultStatus
from blix_agent_worker.tools.runner import ToolRunner


class AgentAskV1Input(ContractModel):
    questions: Annotated[list[QuestionItem], Field(min_length=1, max_length=10)]

    @model_validator(mode="after")
    def validate_question_ids(self):
        ids = [item.id for item in self.questions]
        if len(ids) != len(set(ids)):
            raise ValueError("Question IDs must be unique.")
        for item in self.questions:
            labels = [option.label for option in item.options]
            if len(labels) != len(set(labels)):
                raise ValueError("Option labels must be unique.")
        return self


class AgentAskV1Tool:
    definition = ToolDefinition(
        id="agent_ask_v1",
        description=(
            "Ask the Blix lead a clarification question and pause until it answers. The lead may answer from "
            "verified context or ask the user. Use only for material uncertainty, missing authorization, or "
            "user preferences; do not ask again about decisions already supplied. Returns the actual answers."
        ),
        input_schema=AgentAskV1Input.model_json_schema(),
        impact=ToolImpact.MUTATING,
        idempotent=True,
    )

    def __init__(self, lease: AttemptLease, acknowledge: Callable[[str], None]):
        self.lease = lease
        self.acknowledge = acknowledge

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            arguments = AgentAskV1Input.model_validate(call.arguments)
        except ValidationError:
            return ToolRunner.failure(call, "invalid_arguments", "Invalid clarification questions.")
        digest = hashlib.sha256(
            f"{call.run_id}:{call.parent_session_id}:{call.idempotency_key or call.call_id}".encode()
        ).hexdigest()[:32]
        question = TaskQuestion(id=f"ask_{digest}", questions=arguments.questions)
        commands = await self.lease.post("commands")
        for command in commands.get("commands", []):
            if command["kind"] == "cancel":
                raise asyncio.CancelledError
            if command["kind"] != "answer" or command["payload"].get("question_id") != question.id:
                continue
            answer = TaskAnswer.model_validate(command["payload"])
            answer.validate_question(question)
            self.acknowledge(command["command_id"])
            return ToolResult(
                run_id=call.run_id,
                call_id=call.call_id,
                parent_session_id=call.parent_session_id,
                status=ToolResultStatus.SUCCEEDED,
                output=answer.model_dump(mode="json"),
            )
        raise TaskInputRequiredError(question)
