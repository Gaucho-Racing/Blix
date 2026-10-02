from typing import Annotated

from pydantic import Field

from blix_agent_worker.models.common import ContractModel, DocumentID, JsonObject
from blix_agent_worker.models.tool import ToolCall, ToolDefinition
from blix_agent_worker.tools.knowledge._shared import KnowledgeTool


class KnowledgeDocumentReadV1Input(ContractModel):
    document_id: DocumentID
    chunk: Annotated[int, Field(ge=1)] | None = None


class KnowledgeDocumentReadV1Tool(KnowledgeTool):
    input_model = KnowledgeDocumentReadV1Input
    definition = ToolDefinition(
        id="knowledge_document_read_v1",
        description=(
            "Read an imported document's metadata, page IDs, coverage and current notes. Supply chunk to read "
            "its immutable findings and original page text. Inspect saved findings across relevant chunks "
            "before curating; reopen cited original evidence to verify claims."
        ),
        input_schema=KnowledgeDocumentReadV1Input.model_json_schema(),
    )

    async def _execute(self, call: ToolCall, arguments: KnowledgeDocumentReadV1Input, url: str) -> JsonObject:
        path = f"documents/{arguments.document_id}"
        if arguments.chunk is not None:
            path += f"/chunks/{arguments.chunk}"
        return await self._client.request(url, "GET", path,
            params={"run_id": call.run_id, "session_id": call.parent_session_id},
        )
