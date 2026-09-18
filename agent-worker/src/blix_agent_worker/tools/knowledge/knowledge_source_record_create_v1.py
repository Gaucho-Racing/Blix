from pydantic import ValidationError

from blix_agent_worker.clients.knowledge import KnowledgeClientError
from blix_agent_worker.models.common import JsonObject
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact
from blix_agent_worker.tools.knowledge._shared import KnowledgeTool
from blix_agent_worker.tools.knowledge._sources import CapturedSourceRecord, SourceRecordCapture


class KnowledgeSourceRecordCreateV1Tool(KnowledgeTool):
    input_model = SourceRecordCapture
    definition = ToolDefinition(
        id="knowledge_source_record_create_v1",
        description=(
            "Persist source material already available in context; does not fetch URLs or call provider APIs. "
            "Supply source_id OR a source descriptor to atomically create/reuse the container. Relay original "
            "text as quoted_source; label your own synthesis agent_summary and include its source references "
            "in metadata. Never present a summary as a verbatim quote. Use known original IDs, URLs, authors, "
            "and timestamps; do not invent missing attribution. A summary needs its own distinct external_id, "
            "not the original message ID. Omit external_version to derive a content/metadata fingerprint. "
            "Identical captures deduplicate; a changed explicit version conflicts rather than overwriting. "
            "One Discord channel per source, one message per record; keep thread ID in parent_external_id. "
            "For Blix user chat use knowledge_remember_v1 when available. Do not store secrets or unnecessary "
            "personal data. Returns source_record_id for the curator; capturing alone does not update the wiki."
        ),
        input_schema=SourceRecordCapture.model_json_schema(),
        impact=ToolImpact.MUTATING,
        idempotent=True,
    )

    async def _execute(self, call: ToolCall, arguments: SourceRecordCapture, url: str) -> JsonObject:
        payload = arguments.model_dump(mode="json", exclude_none=True)
        payload.update({
            "capture_method": "agent_relay",
            "capture_run_id": call.run_id,
            "capture_execution_id": call.parent_execution_id,
        })
        body = await self._client.request(url, "POST", "source-records", payload=payload)
        try:
            stored = CapturedSourceRecord.model_validate(body)
        except ValidationError as error:
            raise KnowledgeClientError("invalid_knowledge_response", "Invalid source capture response.") from error
        if (
            stored.record.external_id != arguments.external_id.strip()
            or stored.record.evidence_type != arguments.evidence_type
            or (arguments.source_id is not None and stored.record.source_id != arguments.source_id)
        ):
            raise KnowledgeClientError("invalid_knowledge_response", "The captured record does not match the request.")
        return {**stored.record.model_dump(mode="json"), "created": stored.created, "wiki_status": "not_published"}
