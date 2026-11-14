import asyncio
import json
import os
import re
from typing import Annotated, Self

import vl_convert
from pydantic import Field, JsonValue, ValidationError, field_validator, model_validator

from blix_agent_worker.clients.depot import DepotClient, DepotClientError
from blix_agent_worker.models.common import ContractModel, JsonObject, validation_error_details
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact, ToolResult
from blix_agent_worker.tools.workbench import WorkbenchAccess, WorkbenchToolError
from blix_agent_worker.tools.workbench._shared import failed, failed_from, succeeded

MAX_SPEC_BYTES = 64 * 1024
MAX_INLINE_ROWS = 5000
RENDER_SCALE = 2
DEFAULT_WIDTH = 720
DEFAULT_HEIGHT = 360
_COMPOSITE_KEYS = frozenset({"hconcat", "vconcat", "concat", "facet", "repeat"})


class ChartQuery(ContractModel):
    target_id: Annotated[str, Field(min_length=1, max_length=100)]
    database_name: Annotated[str, Field(min_length=1, max_length=200)] | None = None
    statement: Annotated[str, Field(min_length=1, max_length=100_000)]


class ChartRenderV1Input(ContractModel):
    title: Annotated[str, Field(min_length=1, max_length=120)] = Field(
        description="Short human-readable chart title, also used as the image alt text and file name."
    )
    spec: JsonObject = Field(
        description=(
            "Vega-Lite v6 specification without a data property; the tool injects the rows as inline data. "
            "Reference result columns by name in encodings. Timestamps are UTC: encode them as type temporal "
            "and only use utc-prefixed time units, such as utcyearmonthdatehours for hourly buckets or "
            "utcyearmonthdate for daily buckets. Non-utc time units shift data into the renderer's local time."
        )
    )
    query: ChartQuery | None = Field(
        default=None,
        description="Read-only Workbench query whose rows become the chart data. Prefer this over values.",
    )
    values: Annotated[list[JsonObject], Field(min_length=1, max_length=MAX_INLINE_ROWS)] | None = Field(
        default=None, description="Inline data rows, only for small data that did not come from a single query."
    )

    @field_validator("spec")
    @classmethod
    def spec_must_be_renderable(cls, spec: JsonObject) -> JsonObject:
        if "data" in spec or "datasets" in spec:
            raise ValueError("spec must not contain data or datasets; pass query or values instead")
        if len(json.dumps(spec, separators=(",", ":"))) > MAX_SPEC_BYTES:
            raise ValueError(f"spec must be at most {MAX_SPEC_BYTES} bytes")
        return spec

    @model_validator(mode="after")
    def validate_data_source(self) -> Self:
        if (self.query is None) == (self.values is None):
            raise ValueError("provide exactly one of query or values")
        return self


class ChartRenderV1Tool:
    definition = ToolDefinition(
        id="chart_render_v1",
        description=(
            "Render a PNG chart from a Vega-Lite spec and publish it as a permanent public Depot link. Supply the "
            "data as a read-only Workbench query (rows are fetched as the current user and never pass through "
            "this conversation) or as small inline values. Returns a Markdown image to place in the reply."
        ),
        input_schema=ChartRenderV1Input.model_json_schema(mode="validation"),
        impact=ToolImpact.MUTATING,
    )

    def __init__(self, access: WorkbenchAccess, depot: DepotClient) -> None:
        self._access = access
        self._depot = depot

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = ChartRenderV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return failed(
                call,
                "invalid_arguments",
                "The chart arguments are invalid.",
                details=validation_error_details(error),
            )

        depot_url = os.environ.get("DEPOT_URL", "").strip()
        depot_bucket = os.environ.get("DEPOT_PUBLIC_BUCKET", "").strip()
        depot_token = os.environ.get("SENTINEL_SERVICE_ACCOUNT_TOKEN", "").strip()
        missing = [
            name
            for name, value in {
                "DEPOT_URL": depot_url,
                "DEPOT_PUBLIC_BUCKET": depot_bucket,
                "SENTINEL_SERVICE_ACCOUNT_TOKEN": depot_token,
            }.items()
            if not value
        ]
        if missing:
            return failed(
                call,
                "tool_not_configured",
                f"Tool {self.definition.id!r} is missing required configuration.",
                details={"environment_variables": missing},
            )

        try:
            records = tool_input.values or await self._query_records(call, tool_input.query)
        except WorkbenchToolError as error:
            return failed_from(call, error)
        if not records:
            return failed(call, "chart_data_empty", "The query returned no rows, so there is nothing to chart.")

        spec = self._prepare_spec(tool_input, records)
        try:
            png = await asyncio.to_thread(vl_convert.vegalite_to_png, spec, scale=RENDER_SCALE, allowed_base_urls=[])
        except ValueError as error:
            return failed(call, "invalid_chart_spec", str(error)[:2000])

        try:
            file = await self._depot.upload_public(
                base_url=depot_url,
                bucket=depot_bucket,
                token=depot_token,
                filename=f"{_slug(tool_input.title)}.png",
                content_type="image/png",
                data=png,
                tags={"blix_run_id": call.run_id, "blix_session_id": call.parent_session_id, "kind": "chart"},
            )
        except DepotClientError as error:
            return failed(call, error.code, str(error), retryable=error.retryable)

        alt = tool_input.title.replace("[", "(").replace("]", ")")
        return succeeded(
            call,
            {
                "file_id": file.id,
                "url": file.url,
                "markdown": f"![{alt}]({file.url})",
                "data_rows": len(records),
                "size_bytes": file.size_bytes,
            },
        )

    async def _query_records(self, call: ToolCall, query: ChartQuery) -> list[JsonObject]:
        workbench = await self._access.session(run_id=call.run_id, session_id=call.parent_session_id)
        result = await self._access.query(
            workbench,
            target_id=query.target_id,
            database_name=query.database_name,
            statement=query.statement,
            read_only=True,
        )
        names = [column.name for column in result.columns]
        if len(set(names)) != len(names):
            raise WorkbenchToolError(
                "chart_duplicate_columns", "Every query column needs a unique name; alias duplicate columns."
            )
        return [dict(zip(names, row, strict=False)) for row in result.typed_rows()]

    @staticmethod
    def _prepare_spec(tool_input: ChartRenderV1Input, records: list[JsonObject]) -> dict[str, JsonValue]:
        spec: dict[str, JsonValue] = {
            "$schema": "https://vega.github.io/schema/vega-lite/v6.json",
            "title": tool_input.title,
            "background": "white",
            "padding": 16,
            **tool_input.spec,
            "data": {"values": records},
        }
        if not _COMPOSITE_KEYS.intersection(spec):
            spec.setdefault("width", DEFAULT_WIDTH)
            spec.setdefault("height", DEFAULT_HEIGHT)
        return spec


def _slug(title: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", title.casefold()).strip("-")[:80] or "chart"
