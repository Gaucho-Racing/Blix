from pydantic import ValidationError

from blix_agent_worker.clients.knowledge import KnowledgeClientError
from blix_agent_worker.models.common import JsonObject
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact
from blix_agent_worker.tools.knowledge._shared import KnowledgeTool
from blix_agent_worker.tools.knowledge._sources import SourceDefinition, StoredSource


class KnowledgeSourceCreateV1Tool(KnowledgeTool):
    input_model = SourceDefinition
    definition = ToolDefinition(
        id="knowledge_source_create_v1",
        description=(
            "Create or reuse a source container by kind + external_id. Use kind=discord with a channel ID, "
            "google_drive with a document ID, or another stable origin kind/ID. Not one source per provider, "
            "topic, or arbitrary capture batch. Existing names/metadata are preserved; disabled sources cannot "
            "be reactivated here. This stores no evidence and makes no wiki changes. Safe to retry."
        ),
        input_schema=SourceDefinition.model_json_schema(),
        impact=ToolImpact.MUTATING,
        idempotent=True,
    )

    async def _execute(self, call: ToolCall, arguments: SourceDefinition, url: str) -> JsonObject:
        body = await self._client.request(url, "POST", "sources", payload=arguments.model_dump(mode="json"))
        try:
            source = StoredSource.model_validate(body)
        except ValidationError as error:
            raise KnowledgeClientError("invalid_knowledge_response", "Invalid stored source response.") from error
        if source.kind != arguments.kind or source.external_id != arguments.external_id or source.status != "active":
            raise KnowledgeClientError("invalid_knowledge_response", "The stored source does not match the request.")
        return source.model_dump(mode="json")
