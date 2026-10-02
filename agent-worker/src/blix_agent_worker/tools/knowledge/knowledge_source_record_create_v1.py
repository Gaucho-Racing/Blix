import os

from pydantic import ValidationError

from blix_agent_worker.clients.knowledge import KnowledgeClientError
from blix_agent_worker.clients.sentinel import SentinelClient, SentinelClientError
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
            "Discord authors are resolved to canonical Sentinel identity by the runtime; unlinked authors cannot "
            "be captured. "
            "For Blix user chat use knowledge_remember_v1 when available. Do not store secrets or unnecessary "
            "personal data. Returns source_record_id for the curator; capturing alone does not update the wiki."
        ),
        input_schema=SourceRecordCapture.model_json_schema(),
        impact=ToolImpact.MUTATING,
        idempotent=True,
    )

    def __init__(self, client, sentinel: SentinelClient) -> None:
        super().__init__(client)
        self._sentinel = sentinel

    async def _execute(self, call: ToolCall, arguments: SourceRecordCapture, url: str) -> JsonObject:
        payload = arguments.model_dump(mode="json", exclude_none=True)
        if arguments.author is not None and arguments.author.provider == "discord":
            sentinel_url = os.environ.get("SENTINEL_URL", "").strip() or "https://sso.gauchoracing.com"
            token = os.environ.get("SENTINEL_SERVICE_ACCOUNT_TOKEN", "").strip()
            if not token:
                raise KnowledgeClientError("tool_not_configured", "Sentinel identity resolution is required.")
            try:
                canonical = await self._sentinel.resolve_external_user(
                    sentinel_url=sentinel_url,
                    service_account_token=token,
                    provider="discord",
                    external_id=arguments.author.external_id,
                )
            except SentinelClientError as error:
                raise KnowledgeClientError(error.code, str(error), retryable=error.retryable) from error
            if canonical is None:
                raise KnowledgeClientError(
                    "discord_identity_unresolved",
                    "The Discord author must be linked to a Sentinel user before this evidence can be captured.",
                )
            payload["author"] = {
                **arguments.author.model_dump(mode="json", exclude_none=True),
                "display_name": canonical.full_name,
                "canonical_name": canonical.full_name,
                "entity_id": canonical.entity_id,
                "user_id": canonical.user_id,
            }
        payload.update(
            {
                "capture_method": "agent_relay",
                "capture_conversation_id": call.conversation_id,
                "capture_session_id": call.parent_session_id,
            }
        )
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
