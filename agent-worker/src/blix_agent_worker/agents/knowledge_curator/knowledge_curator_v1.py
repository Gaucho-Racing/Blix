import json
from typing import Annotated, Literal, Self

from pydantic import Field, model_validator

from blix_agent_worker.agents.knowledge_curator.prompt import (
    KNOWLEDGE_CURATOR_V1_SYSTEM_PROMPT,
    KNOWLEDGE_CURATOR_V1_USER_PROMPT,
)
from blix_agent_worker.agents.tool_agent import ToolAgent
from blix_agent_worker.models.agent import AgentDefinition, AgentKind, AgentTask
from blix_agent_worker.models.common import ChangesetID, ContractModel, NonEmptyText, SourceRecordID
from blix_agent_worker.models.llm import ModelMessage, ModelMessageRole
from blix_agent_worker.models.tool import ToolDefinition, ToolResult, ToolResultStatus
from blix_agent_worker.tools.knowledge._shared import CommitSHA, PagePath


class KnowledgeCuratorV1Input(ContractModel):
    source_record_ids: Annotated[tuple[SourceRecordID, ...], Field(min_length=1, max_length=100)]
    mode: Literal["propose", "apply"] = "propose"
    season_hint: Annotated[str, Field(max_length=64)] = ""

    @model_validator(mode="after")
    def unique_sources(self) -> Self:
        if len(set(self.source_record_ids)) != len(self.source_record_ids):
            raise ValueError("source_record_ids must be unique")
        return self


class CuratedChangeset(ContractModel):
    changeset_id: ChangesetID
    status: Literal["proposed", "committed"]
    commit_sha: CommitSHA
    paths: Annotated[tuple[PagePath, ...], Field(min_length=1)]

    @model_validator(mode="after")
    def validate_commit(self) -> Self:
        if (self.status == "committed") != bool(self.commit_sha):
            raise ValueError("only committed changesets must have a commit SHA")
        return self


class KnowledgeCuratorV1Output(ContractModel):
    summary: NonEmptyText
    outcome: Literal["proposed", "committed", "no_changes", "blocked"]
    source_record_ids: tuple[SourceRecordID, ...]
    changesets: tuple[CuratedChangeset, ...]
    warnings: tuple[NonEmptyText, ...]

    @model_validator(mode="after")
    def validate_outcome(self) -> Self:
        if self.outcome in {"proposed", "committed"} and (
            not self.changesets or any(c.status != self.outcome for c in self.changesets)
        ):
            raise ValueError("outcome must match reported changesets")
        if self.outcome == "no_changes" and self.changesets:
            raise ValueError("no_changes cannot report changesets")
        if len({c.changeset_id for c in self.changesets}) != len(self.changesets):
            raise ValueError("changesets must be unique")
        return self


class KnowledgeCuratorV1Agent(ToolAgent):
    input_model = KnowledgeCuratorV1Input
    output_model = KnowledgeCuratorV1Output
    definition = AgentDefinition(
        id="knowledge_curator_v1",
        kind=AgentKind.SPECIALIST,
        description=(
            "Curate stored source records into Blix's Markdown wiki with citations. Supports shared services, "
            "administration, and car-specific knowledge without requiring a season. Pass existing "
            "sourcerecord_ IDs. Defaults to propose-only; use mode=apply to publish GitHub changesets."
        ),
        instructions=KNOWLEDGE_CURATOR_V1_SYSTEM_PROMPT,
        input_schema=KnowledgeCuratorV1Input.model_json_schema(),
        output_schema=KnowledgeCuratorV1Output.model_json_schema(),
        model_profile="knowledge_curator",
        tool_ids=("knowledge_read_v1", "knowledge_propose_v1", "knowledge_apply_v1"),
    )

    def initial_messages(self, task: AgentTask, agent_input: ContractModel) -> list[ModelMessage]:
        prompt = KNOWLEDGE_CURATOR_V1_USER_PROMPT.format(
            objective=task.objective,
            request=agent_input.model_dump_json(),
            context=json.dumps(task.context, ensure_ascii=False),
        )
        return [
            ModelMessage(role=ModelMessageRole.SYSTEM, content=self.definition.instructions),
            ModelMessage(role=ModelMessageRole.USER, content=prompt),
        ]

    def tool_definitions(self, agent_input: KnowledgeCuratorV1Input) -> tuple[ToolDefinition, ...]:
        ids = self.definition.tool_ids
        if agent_input.mode == "propose":
            ids = tuple(tool_id for tool_id in ids if tool_id != "knowledge_apply_v1")
        return self._tool_runner.registry.definitions(ids)

    def validate_output(self, task: AgentTask, output: KnowledgeCuratorV1Output, messages: list[ModelMessage]) -> None:
        expected_sources = set(KnowledgeCuratorV1Input.model_validate(task.input).source_record_ids)
        if set(output.source_record_ids) != expected_sources or len(output.source_record_ids) != len(expected_sources):
            raise ValueError("result must identify exactly the supplied source records")
        receipts = {}
        read_sources = set()
        for message in messages:
            if message.role is not ModelMessageRole.CALL_OUTPUT or not message.content:
                continue
            result = ToolResult.model_validate_json(message.content)
            if result.status is not ToolResultStatus.SUCCEEDED or not isinstance(result.output, dict):
                continue
            if isinstance(result.output.get("source_record_id"), str):
                read_sources.add(result.output["source_record_id"])
            changeset = result.output.get("changeset")
            if isinstance(changeset, dict):
                receipts[changeset.get("changeset_id")] = result.output
        if output.outcome != "blocked" and not expected_sources.issubset(read_sources):
            raise ValueError("the curator must read its source records before reporting completion")
        for reported in output.changesets:
            receipt = receipts.get(reported.changeset_id, {})
            actual = receipt.get("changeset", {})
            files = receipt.get("files", [])
            if (
                actual.get("run_id") != task.run_id
                or actual.get("execution_id") != task.execution_id
                or actual.get("status") != reported.status
                or (actual.get("result_commit_sha") or "") != reported.commit_sha
                or {file.get("path") for file in files} != set(reported.paths)
            ):
                raise ValueError("reported changeset does not match a successful knowledge tool response")
