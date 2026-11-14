from typing import Annotated, Literal, Self
from urllib.parse import quote

from pydantic import BaseModel, ConfigDict, Field, JsonValue, ValidationError, model_validator

from blix_agent_worker.clients.workbench import WorkbenchClientError
from blix_agent_worker.models.common import ContractModel, JsonObject, validation_error_details
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult
from blix_agent_worker.tools.workbench._shared import (
    WorkbenchAccess,
    WorkbenchSession,
    WorkbenchToolError,
    failed,
    failed_from,
    succeeded,
    translate_workbench_error,
)

MAX_CATALOG_TABLES = 150


class WorkbenchCatalogV1Input(ContractModel):
    operation: Literal["list_targets", "list_databases", "describe"] = Field(
        description=(
            "list_targets lists database connections the user can query. list_databases lists databases on a "
            "target. describe returns tables, views, and columns for one database on a target."
        )
    )
    target_id: Annotated[str, Field(min_length=1, max_length=100)] | None = None
    database_name: Annotated[str, Field(min_length=1, max_length=200)] | None = Field(
        default=None, description="Database to inspect; defaults to the target's configured database."
    )
    schema_name: Annotated[str, Field(min_length=1, max_length=200)] | None = Field(
        default=None, description="describe only: limit results to one schema, such as public."
    )
    search: Annotated[str, Field(min_length=1, max_length=200)] | None = Field(
        default=None,
        description=(
            "describe only: case-insensitive substring matched against table and column names, such as "
            "'message' or 'discord'. Tables matching by name include all columns; otherwise only matching columns."
        ),
    )
    include_foreign_keys: bool = False

    @model_validator(mode="after")
    def validate_operation(self) -> Self:
        if self.operation != "list_targets" and self.target_id is None:
            raise ValueError(f"target_id is required for {self.operation}")
        if self.operation != "describe" and (self.schema_name or self.search or self.include_foreign_keys):
            raise ValueError("schema_name, search, and include_foreign_keys apply only to describe")
        return self


class _CatalogColumn(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    name: str
    data_type: str
    nullable: bool
    primary_key: bool


class _CatalogTable(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    schema_name: str = Field(alias="schema")
    name: str
    kind: str
    columns: tuple[_CatalogColumn, ...] = ()


class _CatalogForeignKey(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    source_schema: str
    source_table: str
    source_column: str
    target_schema: str
    target_table: str
    target_column: str


class _Catalog(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    tables: tuple[_CatalogTable, ...] = ()
    foreign_keys: tuple[_CatalogForeignKey, ...] = ()


class _Target(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    id: str
    name: str
    environment: str = ""
    database_name: str = ""


class WorkbenchCatalogV1Tool:
    definition = ToolDefinition(
        id="workbench_catalog_v1",
        description=(
            "Discover what can be queried in Workbench, Gaucho Racing's database tool, acting as the current user. "
            "Start with list_targets, then describe a target with search or schema_name to find relevant tables "
            "and columns before writing SQL for workbench_query_v1."
        ),
        input_schema=WorkbenchCatalogV1Input.model_json_schema(mode="validation"),
        idempotent=True,
    )

    def __init__(self, access: WorkbenchAccess) -> None:
        self._access = access

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = WorkbenchCatalogV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return failed(
                call,
                "invalid_arguments",
                "The Workbench catalog arguments are invalid.",
                details=validation_error_details(error),
            )
        try:
            workbench = await self._access.session(run_id=call.run_id, session_id=call.parent_session_id)
            match tool_input.operation:
                case "list_targets":
                    payload = await self._get(workbench, "/targets")
                    targets = [_Target.model_validate(item).model_dump() for item in _as_list(payload)]
                    return succeeded(call, {"targets": targets})
                case "list_databases":
                    payload = await self._get(workbench, f"/targets/{quote(tool_input.target_id, safe='')}/databases")
                    names = [item["name"] for item in _as_list(payload) if isinstance(item, dict) and "name" in item]
                    return succeeded(call, {"target_id": tool_input.target_id, "databases": names})
                case "describe":
                    query = {"database": tool_input.database_name} if tool_input.database_name else None
                    payload = await self._get(
                        workbench, f"/targets/{quote(tool_input.target_id, safe='')}/catalog", query
                    )
                    return succeeded(call, self._describe(tool_input, _Catalog.model_validate(payload)))
        except WorkbenchToolError as error:
            return failed_from(call, error)
        except ValidationError:
            return failed(call, "invalid_workbench_response", "Workbench returned an unexpected catalog shape.")

    async def _get(self, workbench: WorkbenchSession, path: str, query: dict[str, str] | None = None) -> JsonValue:
        try:
            return await self._access.client.get(
                base_url=workbench.base_url, access_token=workbench.access_token, path=path, query=query
            )
        except WorkbenchClientError as error:
            raise translate_workbench_error(error) from error

    @staticmethod
    def _describe(tool_input: WorkbenchCatalogV1Input, catalog: _Catalog) -> JsonObject:
        needle = (tool_input.search or "").casefold()
        tables: list[JsonObject] = []
        matched = 0
        for table in catalog.tables:
            if tool_input.schema_name and table.schema_name != tool_input.schema_name:
                continue
            columns = table.columns
            if needle and needle not in table.name.casefold():
                columns = tuple(column for column in columns if needle in column.name.casefold())
                if not columns:
                    continue
            matched += 1
            if len(tables) >= MAX_CATALOG_TABLES:
                continue
            tables.append(
                {
                    "table": f"{table.schema_name}.{table.name}",
                    "kind": table.kind,
                    "columns": [
                        {
                            "name": column.name,
                            "type": column.data_type,
                            **({"nullable": True} if column.nullable else {}),
                            **({"primary_key": True} if column.primary_key else {}),
                        }
                        for column in columns
                    ],
                }
            )

        output: JsonObject = {
            "target_id": tool_input.target_id,
            "database_name": tool_input.database_name,
            "tables": tables,
            "matched_tables": matched,
            "truncated": matched > len(tables),
        }
        if tool_input.include_foreign_keys:
            names = {table["table"] for table in tables}
            output["foreign_keys"] = [
                f"{key.source_schema}.{key.source_table}.{key.source_column} -> "
                f"{key.target_schema}.{key.target_table}.{key.target_column}"
                for key in catalog.foreign_keys
                if f"{key.source_schema}.{key.source_table}" in names
            ]
        return output


def _as_list(payload: object) -> list:
    if not isinstance(payload, list):
        raise WorkbenchToolError("invalid_workbench_response", "Workbench returned an unexpected list shape.")
    return payload
