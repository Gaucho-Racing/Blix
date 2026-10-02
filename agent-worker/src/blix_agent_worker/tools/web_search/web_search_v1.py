import os
from typing import Annotated, Literal

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


class WebSearchV1Input(ContractModel):
    query: NonEmptyText
    max_results: Annotated[int, Field(ge=1, le=10)] = 5
    topic: Literal["general", "news", "finance"] = "general"
    time_range: Literal["day", "week", "month", "year"] | None = None


class WebSearchResult(ContractModel):
    title: NonEmptyText
    url: HttpUrl
    content: NonEmptyText
    score: Annotated[float, Field(ge=0, le=1)]
    published_date: NonEmptyText | None = None


class WebSearchV1Output(ContractModel):
    query: NonEmptyText
    results: tuple[WebSearchResult, ...]


class _TavilySearchResult(BaseModel):
    model_config = ConfigDict(extra="ignore")

    title: str
    url: str
    content: str
    score: float
    published_date: str | None = None


class _TavilySearchResponse(BaseModel):
    model_config = ConfigDict(extra="ignore")

    query: str
    results: tuple[_TavilySearchResult, ...]


class WebSearchV1Tool:
    definition = ToolDefinition(
        id="web_search_v1",
        description="Search the public web and return ranked source snippets with URLs.",
        input_schema=WebSearchV1Input.model_json_schema(mode="validation"),
        output_schema=WebSearchV1Output.model_json_schema(mode="serialization"),
        idempotent=True,
    )

    def __init__(self, client: TavilyClient) -> None:
        self._client = client

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = WebSearchV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return self._failure(
                call,
                "invalid_arguments",
                "The web search arguments are invalid.",
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
            payload: JsonObject = {
                "query": tool_input.query,
                "search_depth": "basic",
                "max_results": tool_input.max_results,
                "topic": tool_input.topic,
                "include_answer": False,
                "include_raw_content": False,
                "include_images": False,
                "safe_search": True,
            }
            if tool_input.time_range is not None:
                payload["time_range"] = tool_input.time_range
            body = await self._client.search(payload, api_key=api_key)
            response = _TavilySearchResponse.model_validate(body)
            output = WebSearchV1Output(
                query=response.query,
                results=tuple(
                    WebSearchResult(
                        title=result.title,
                        url=result.url,
                        content=result.content,
                        score=result.score,
                        published_date=result.published_date,
                    )
                    for result in response.results
                ),
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
                "The web provider returned an invalid search response.",
            )

        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_session_id=call.parent_session_id,
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
            parent_session_id=call.parent_session_id,
            status=ToolResultStatus.FAILED,
            error=ContractError(
                code=code,
                message=message,
                retryable=retryable,
                details=details or {},
            ),
        )
