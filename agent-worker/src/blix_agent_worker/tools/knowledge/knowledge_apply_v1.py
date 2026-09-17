from blix_agent_worker.clients.knowledge import KnowledgeClientError
from blix_agent_worker.models.common import ChangesetID, ContractModel, JsonObject
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact
from blix_agent_worker.tools.knowledge._shared import KnowledgeTool


class KnowledgeApplyV1Input(ContractModel):
    changeset_id: ChangesetID


class KnowledgeApplyV1Tool(KnowledgeTool):
    input_model = KnowledgeApplyV1Input
    definition = ToolDefinition(
        id="knowledge_apply_v1",
        description=(
            "Publish a previously proposed wiki changeset from this run and agent execution. "
            "May replace or delete pages. On conflict reread and propose again; never force writes. "
            "Retry the same changeset ID after transient failures to avoid duplicate commits."
        ),
        input_schema=KnowledgeApplyV1Input.model_json_schema(),
        impact=ToolImpact.DESTRUCTIVE,
        idempotent=True,
    )

    async def _execute(self, call: ToolCall, arguments: KnowledgeApplyV1Input, url: str) -> JsonObject:
        path = f"changesets/{arguments.changeset_id}"
        result = await self._client.request(url, "GET", path)
        changeset = result.get("changeset")
        if not isinstance(changeset, dict) or changeset.get("changeset_id") != arguments.changeset_id:
            raise KnowledgeClientError(
                "invalid_knowledge_response", "The knowledge service returned an invalid changeset."
            )
        if changeset.get("run_id") != call.run_id or changeset.get("execution_id") != call.parent_execution_id:
            raise KnowledgeClientError(
                "changeset_not_owned", "Only changesets from this run and execution may be applied."
            )
        return await self._client.request(url, "POST", f"{path}/apply")
