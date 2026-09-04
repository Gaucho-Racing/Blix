import os
from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field, HttpUrl, ValidationError

from blix_agent_worker.clients import TavilyClient, TavilyClientError
from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    JsonObject,
    NonEmptyText,
    validation_error_details,
)
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult, ToolResultStatus


class WebExtractV1Input(ContractModel):
    urls: Annotated[tuple[HttpUrl, ...], Field(min_length=1, max_length=5)]
    query: NonEmptyText
    chunks_per_source: Annotated[int, Field(ge=1, le=5)] = 3


class WebExtractResult(ContractModel):
    url: HttpUrl
    content: NonEmptyText


class WebExtractFailure(ContractModel):
    url: HttpUrl
    error: NonEmptyText


class WebExtractV1Output(ContractModel):
    results: tuple[WebExtractResult, ...]
    failures: tuple[WebExtractFailure, ...]


class _TavilyExtractResult(BaseModel):
    model_config = ConfigDict(extra="ignore")

    url: str
    raw_content: str


class _TavilyExtractFailure(BaseModel):
    model_config = ConfigDict(extra="ignore")

    url: str
    error: str


class _TavilyExtractResponse(BaseModel):
    model_config = ConfigDict(extra="ignore")

    results: tuple[_TavilyExtractResult, ...]
    failed_results: tuple[_TavilyExtractFailure, ...] = ()


class WebExtractV1Tool:
    definition = ToolDefinition(
        id="web_extract_v1",
        description="Extract query-relevant text from public web pages returned by web search.",
        input_schema=WebExtractV1Input.model_json_schema(mode="validation"),
        output_schema=WebExtractV1Output.model_json_schema(mode="serialization"),
        idempotent=True,
    )

    def __init__(self, client: TavilyClient) -> None:
        self._client = client

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = WebExtractV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return self._failure(
                call,
                "invalid_arguments",
                "The web extraction arguments are invalid.",
                details=validation_error_details(error),
            )

        api_key = os.environ.get("TAVILY_API_KEY", "").strip()
        if not api_key:
            return self._failure(
                call,
                "tool_not_configured",
                f"Tool {self.definition.id!r} requires TAVILY_API_KEY.",
                details={"environment_variables": ["TAVILY_API_KEY"]},
            )

        try:
            body = await self._client.extract(
                {
                    "urls": [str(url) for url in tool_input.urls],
                    "query": tool_input.query,
                    "chunks_per_source": tool_input.chunks_per_source,
                    "extract_depth": "basic",
                    "include_images": False,
                    "format": "markdown",
                },
                api_key=api_key,
            )
            response = _TavilyExtractResponse.model_validate(body)
            extracted_results: list[WebExtractResult] = []
            extraction_failures = [
                WebExtractFailure(url=result.url, error=result.error) for result in response.failed_results
            ]
            for result in response.results:
                content = result.raw_content.strip()
                if content:
                    extracted_results.append(WebExtractResult(url=result.url, content=content))
                else:
                    extraction_failures.append(
                        WebExtractFailure(url=result.url, error="The provider returned no extractable content.")
                    )
            output = WebExtractV1Output(
                results=tuple(extracted_results),
                failures=tuple(extraction_failures),
            )
        except TavilyClientError as error:
            if error.code == "web_provider_authentication_failed":
                return self._failure(
                    call,
                    "tool_not_configured",
                    f"Tool {self.definition.id!r} has invalid Tavily credentials.",
                    details={"environment_variables": ["TAVILY_API_KEY"]},
                )
            return self._failure(call, error.code, str(error), retryable=error.retryable)
        except ValidationError:
            return self._failure(
                call,
                "invalid_web_provider_response",
                "The web provider returned an invalid extraction response.",
            )

        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_execution_id=call.parent_execution_id,
            status=ToolResultStatus.SUCCEEDED,
            output=output.model_dump(mode="json"),
        )

    @staticmethod
    def _failure(
        call: ToolCall,
        code: str,
        message: str,
        *,
        retryable: bool = False,
        details: JsonObject | None = None,
    ) -> ToolResult:
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_execution_id=call.parent_execution_id,
            status=ToolResultStatus.FAILED,
            error=ContractError(
                code=code,
                message=message,
                retryable=retryable,
                details=details or {},
            ),
        )
