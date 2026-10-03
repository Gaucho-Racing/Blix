from typing import Annotated, Literal, Self

from pydantic import ConfigDict, Field, field_validator, model_validator

from blix_agent_worker.models.common import ContractModel, JsonObject, SourceRecordID
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact
from blix_agent_worker.tools.knowledge._shared import CommitSHA, KnowledgeTool, PagePath, validate_page_path


class KnowledgeFileEdit(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)
    operation: Literal["upsert", "delete"]
    path: PagePath
    content: str = ""

    _path = field_validator("path")(validate_page_path)

    @model_validator(mode="after")
    def validate_content(self) -> Self:
        if self.operation == "delete" and self.content:
            raise ValueError("deletes cannot contain content")
        if "\x00" in self.content or len(self.content.encode("utf-8")) > 1024 * 1024:
            raise ValueError("content must be UTF-8 text up to 1 MiB")
        return self


class KnowledgeProposeV1Input(ContractModel):
    summary: Annotated[str, Field(min_length=1, max_length=500)]
    base_commit_sha: CommitSHA
    type: Literal["content", "structural"] = "content"
    season_slug: Annotated[str, Field(pattern=r"^([a-z0-9][a-z0-9_-]{0,63})?$")] = ""
    source_record_ids: Annotated[tuple[SourceRecordID, ...], Field(min_length=1, max_length=500)]
    files: Annotated[tuple[KnowledgeFileEdit, ...], Field(min_length=1, max_length=100)]

    @model_validator(mode="after")
    def validate_proposal(self) -> Self:
        if any(c in self.summary for c in "\r\n\x00"):
            raise ValueError("summary must be one line")
        if len({f.path for f in self.files}) != len(self.files):
            raise ValueError("file paths must be unique")
        if len(set(self.source_record_ids)) != len(self.source_record_ids):
            raise ValueError("source record IDs must be unique")
        if sum(len(f.content.encode("utf-8")) for f in self.files) > 4 * 1024 * 1024:
            raise ValueError("changeset exceeds 4 MiB")
        for file in self.files:
            if file.operation == "upsert" and not any(ref in file.content for ref in self.source_record_ids):
                raise ValueError("each updated page must cite at least one supplied source record ID")
        return self


class KnowledgeProposeV1Tool(KnowledgeTool):
    input_model = KnowledgeProposeV1Input
    definition = ToolDefinition(
        id="knowledge_propose_v1",
        description=(
            "Persist a permanent memory changeset without publishing. Read relevant pages first and supply their base "
            "commit SHA (empty only for empty permanent memory). Files are complete Markdown upserts or deletes. "
            "Cite source record IDs in updated pages; records are checked before proposing. Leave "
            "season_slug empty for shared services, administration, or cross-season changes. "
            "Run and execution provenance is attached by the runtime. Use knowledge_apply_v1 to publish."
        ),
        input_schema=KnowledgeProposeV1Input.model_json_schema(),
        impact=ToolImpact.MUTATING,
    )

    async def _execute(self, call: ToolCall, arguments: KnowledgeProposeV1Input, url: str) -> JsonObject:
        payload = arguments.model_dump(mode="json")
        payload.update(run_id=call.run_id, session_id=call.parent_session_id)
        return await self._client.request(url, "POST", "changesets", payload=payload)
