import os
import posixpath
from typing import Annotated, ClassVar

from pydantic import Field, HttpUrl, TypeAdapter, ValidationError

from blix_agent_worker.clients.knowledge import KnowledgeClient, KnowledgeClientError
from blix_agent_worker.models.common import ContractError, ContractModel, JsonObject, validation_error_details
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult, ToolResultStatus

PagePath = Annotated[str, Field(min_length=4, max_length=512, pattern=r"^[a-zA-Z0-9_/-][a-zA-Z0-9_./-]*\.md$")]
CommitSHA = Annotated[str, Field(pattern=r"^([0-9a-f]{40})?$")]


def validate_page_path(value: str) -> str:
    if value.startswith("/") or posixpath.normpath(value) != value or any(p.startswith(".") for p in value.split("/")):
        raise ValueError("page path must be a relative Markdown path without hidden or traversal components")
    return value


class KnowledgeTool:
    definition: ClassVar[ToolDefinition]
    input_model: ClassVar[type[ContractModel]]

    def __init__(self, client: KnowledgeClient) -> None:
        self._client = client

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            arguments = self.input_model.model_validate(call.arguments)
        except ValidationError as error:
            return self._failure(
                call, "invalid_arguments", "Invalid knowledge tool arguments.", details=validation_error_details(error)
            )
        try:
            url = TypeAdapter(HttpUrl).validate_python(os.environ.get("KNOWLEDGE_URL", "").strip())
            if url.username or url.password or url.query or url.fragment:
                raise ValueError("credentials, query, and fragment are not supported")
        except (ValueError, ValidationError):
            return self._failure(call, "tool_not_configured", "A valid KNOWLEDGE_URL is required.")
        try:
            output = await self._execute(call, arguments, str(url))
        except KnowledgeClientError as error:
            return self._failure(call, error.code, str(error), retryable=error.retryable)
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_execution_id=call.parent_execution_id,
            status=ToolResultStatus.SUCCEEDED,
            output=output,
        )

    async def _execute(self, call: ToolCall, arguments: ContractModel, url: str) -> JsonObject:
        raise NotImplementedError

    @staticmethod
    def _failure(
        call: ToolCall, code: str, message: str, *, retryable: bool = False, details: JsonObject | None = None
    ) -> ToolResult:
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_execution_id=call.parent_execution_id,
            status=ToolResultStatus.FAILED,
            error=ContractError(code=code, message=message, retryable=retryable, details=details or {}),
        )
