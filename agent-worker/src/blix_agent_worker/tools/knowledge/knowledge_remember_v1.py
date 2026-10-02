from pydantic import BaseModel, ConfigDict, Field, ValidationError

from blix_agent_worker.clients.knowledge import KnowledgeClient, KnowledgeClientError
from blix_agent_worker.models.common import ContractModel, JsonObject, MessageID, SourceID, SourceRecordID
from blix_agent_worker.models.context import CallerContext, EntityType
from blix_agent_worker.models.message import RunMessage, RunMessageRole
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact
from blix_agent_worker.tools.knowledge._shared import KnowledgeTool


class KnowledgeRememberV1Input(ContractModel):
    message_id: MessageID | None = Field(
        default=None,
        description="Original user message ID from this run. Omit to capture the latest user message.",
    )


class _StoredChatRecord(BaseModel):
    model_config = ConfigDict(extra="ignore")

    source_id: SourceID
    source_record_id: SourceRecordID


class _RememberResponse(BaseModel):
    model_config = ConfigDict(extra="ignore")

    record: _StoredChatRecord
    created: bool


class KnowledgeRememberV1Tool(KnowledgeTool):
    input_model = KnowledgeRememberV1Input
    definition = ToolDefinition(
        id="knowledge_remember_v1",
        description=(
            "Capture an original user chat message verbatim as attributed evidence when remembering durable team "
            "knowledge. Supply message_id from the run message index to capture an earlier message, "
            "or omit it for the latest message. Call once per source message when remembering multiple messages. "
            "Text, caller, timestamp, run and execution IDs come from the runtime, not model-supplied content. "
            "Stores a source record only, not a wiki update. Delegate its returned source_record_id to "
            "knowledge_curator_v1, normally in propose mode for unsupported human claims. Do not capture secrets "
            "or sensitive personal information into the shared knowledge store. Safe to retry."
        ),
        input_schema=KnowledgeRememberV1Input.model_json_schema(),
        impact=ToolImpact.MUTATING,
        idempotent=True,
    )

    def __init__(
        self, client: KnowledgeClient, messages: tuple[RunMessage, ...], caller: CallerContext
    ) -> None:
        super().__init__(client)
        self._messages = {message.message_id: message for message in messages}
        self._latest_message_id = next(
            (message.message_id for message in reversed(messages) if message.role is RunMessageRole.USER), None
        )
        self._caller = caller

    async def _execute(self, call: ToolCall, arguments: KnowledgeRememberV1Input, url: str) -> JsonObject:
        message_id = arguments.message_id or self._latest_message_id
        message = self._messages.get(message_id)
        if (
            message is None
            or message.role is not RunMessageRole.USER
            or message.run_id != call.run_id
            or message.session_id != call.parent_session_id
        ):
            raise KnowledgeClientError(
                "chat_context_unavailable",
                "The selected message must be an original user message from this run and run execution.",
            )
        if self._caller.entity_type is not EntityType.USER or self._caller.user_id is None:
            raise KnowledgeClientError("invalid_caller", "Remembering chat knowledge requires a user caller.")
        if not message.content.strip() or len(message.content.encode("utf-8")) > 1024 * 1024:
            raise KnowledgeClientError("invalid_knowledge_request", "The message must contain text up to 1 MiB.")
        response = await self._client.request(
            url,
            "POST",
            "chat-records",
            payload={
                "run_id": call.run_id,
                "session_id": call.parent_session_id,
                "message_id": message.message_id,
                "entity_id": self._caller.entity_id,
                "user_id": self._caller.user_id,
                "content": message.content,
                "occurred_at": message.created_at.isoformat(),
            },
        )
        try:
            stored = _RememberResponse.model_validate(response)
        except ValidationError as error:
            raise KnowledgeClientError(
                "invalid_knowledge_response", "The knowledge service returned an invalid record."
            ) from error
        return {
            "source_id": stored.record.source_id,
            "source_record_id": stored.record.source_record_id,
            "message_id": message.message_id,
            "created": stored.created,
            "evidence_type": "human_statement",
            "wiki_status": "not_published",
            "recommended_mode": "propose",
        }
