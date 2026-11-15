from typing import Annotated

from pydantic import Field, ValidationError

from blix_agent_worker.models.common import ContractModel, validation_error_details
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact, ToolResult
from blix_agent_worker.tools.workbench._shared import (
    WorkbenchAccess,
    WorkbenchToolError,
    failed,
    failed_from,
    succeeded,
)

DEFAULT_RESULT_ROWS = 50
MAX_RESULT_ROWS = 200


class WorkbenchQueryV1Input(ContractModel):
    target_id: Annotated[str, Field(min_length=1, max_length=100)]
    database_name: Annotated[str, Field(min_length=1, max_length=200)] | None = None
    statement: Annotated[str, Field(min_length=1, max_length=100_000)] = Field(
        description=(
            "One SQL statement in the target's dialect (PostgreSQL or ClickHouse, per its engine). Aggregate in SQL "
            "instead of returning raw rows."
        )
    )
    read_only: bool = Field(
        default=True,
        description=(
            "Keep true for every query unless the user explicitly confirmed this exact write after seeing its "
            "dry-run preview in this conversation."
        ),
    )
    max_rows: Annotated[int, Field(ge=1, le=MAX_RESULT_ROWS)] = DEFAULT_RESULT_ROWS


class WorkbenchQueryV1Tool:
    definition = ToolDefinition(
        id="workbench_query_v1",
        description=(
            "Run one SQL statement through Workbench as the current user and return up to max_rows rows. Write "
            "PostgreSQL for postgres targets and ClickHouse SQL for clickhouse targets. Workbench enforces the "
            "user's own access: read-only by default, writes only for WorkbenchAdmins with read_only=false. Before "
            "any INSERT, UPDATE, DELETE, ALTER, DDL, or other write, first run a read-only dry run (for example a "
            "count and a sample of the rows the statement would touch), show the user the exact statement and its "
            "expected impact, and wait for explicit confirmation in a later message. On postgres targets, cast "
            "numeric results to double precision or bigint so values are numbers."
        ),
        input_schema=WorkbenchQueryV1Input.model_json_schema(mode="validation"),
        impact=ToolImpact.DESTRUCTIVE,
    )

    def __init__(self, access: WorkbenchAccess) -> None:
        self._access = access

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = WorkbenchQueryV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return failed(
                call,
                "invalid_arguments",
                "The Workbench query arguments are invalid.",
                details=validation_error_details(error),
            )
        try:
            workbench = await self._access.session(run_id=call.run_id, session_id=call.parent_session_id)
            result = await self._access.query(
                workbench,
                target_id=tool_input.target_id,
                database_name=tool_input.database_name,
                statement=tool_input.statement,
                read_only=tool_input.read_only,
            )
        except WorkbenchToolError as error:
            return failed_from(call, error)

        rows = result.typed_rows()
        return succeeded(
            call,
            {
                "workbench_run_id": result.run_id,
                "database_name": result.database_name,
                "read_only": tool_input.read_only,
                "command_tag": result.command_tag,
                "columns": [column.name for column in result.columns],
                "rows": rows[: tool_input.max_rows],
                "returned_rows": min(len(rows), tool_input.max_rows),
                "row_count": result.row_count,
                "rows_omitted": max(len(rows) - tool_input.max_rows, 0),
                "workbench_truncated": result.truncated,
                "duration_ms": result.duration_ms,
            },
        )
