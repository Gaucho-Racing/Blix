import os
from dataclasses import dataclass
from decimal import Decimal, InvalidOperation

from pydantic import BaseModel, ConfigDict, HttpUrl, JsonValue, TypeAdapter, ValidationError

from blix_agent_worker.clients import SentinelClient, SentinelClientError
from blix_agent_worker.clients.workbench import WorkbenchClient, WorkbenchClientError
from blix_agent_worker.models.common import ContractError, JsonObject, RunID, SessionID, UserID
from blix_agent_worker.models.tool import ToolCall, ToolResult, ToolResultStatus

DEFAULT_SENTINEL_URL = "https://sso.gauchoracing.com"
_HTTP_URL_VALIDATOR = TypeAdapter(HttpUrl)

_INTEGER_TYPE_OIDS = frozenset({20, 21, 23, 26})
_FLOAT_TYPE_OIDS = frozenset({700, 701, 1700})


class WorkbenchToolError(Exception):
    def __init__(
        self,
        code: str,
        message: str,
        *,
        retryable: bool = False,
        details: JsonObject | None = None,
    ) -> None:
        self.code = code
        self.retryable = retryable
        self.details = details or {}
        super().__init__(message)


@dataclass(frozen=True, slots=True)
class WorkbenchSession:
    base_url: str
    access_token: str


class WorkbenchColumn(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    name: str
    data_type_oid: int


class WorkbenchQueryResult(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    run_id: str
    database_name: str = ""
    columns: tuple[WorkbenchColumn, ...] = ()
    rows: tuple[tuple[JsonValue, ...], ...] = ()
    command_tag: str = ""
    row_count: int = 0
    duration_ms: int = 0
    truncated: bool = False

    def typed_rows(self) -> list[list[JsonValue]]:
        oids = [column.data_type_oid for column in self.columns]
        return [[_coerce(value, oid) for value, oid in zip(row, oids, strict=False)] for row in self.rows]


def _coerce(value: JsonValue, oid: int) -> JsonValue:
    if not isinstance(value, str):
        return value
    if oid in _INTEGER_TYPE_OIDS:
        try:
            return int(value)
        except ValueError:
            return value
    if oid in _FLOAT_TYPE_OIDS:
        try:
            number = Decimal(value)
        except InvalidOperation:
            return value
        return float(number) if number.is_finite() else value
    return value


class WorkbenchAccess:
    def __init__(self, workbench: WorkbenchClient, sentinel: SentinelClient, caller_user_id: UserID) -> None:
        self.client = workbench
        self._sentinel = sentinel
        self._caller_user_id = caller_user_id

    async def session(self, *, run_id: RunID, session_id: SessionID) -> WorkbenchSession:
        workbench_url = os.environ.get("WORKBENCH_URL", "").strip()
        workbench_client_id = os.environ.get("WORKBENCH_CLIENT_ID", "").strip()
        service_account_token = os.environ.get("SENTINEL_SERVICE_ACCOUNT_TOKEN", "").strip()
        missing = [
            name
            for name, value in {
                "WORKBENCH_URL": workbench_url,
                "WORKBENCH_CLIENT_ID": workbench_client_id,
                "SENTINEL_SERVICE_ACCOUNT_TOKEN": service_account_token,
            }.items()
            if not value
        ]
        if missing:
            raise WorkbenchToolError(
                "tool_not_configured",
                "Workbench access is missing required configuration.",
                details={"environment_variables": missing},
            )
        try:
            workbench_url = str(_HTTP_URL_VALIDATOR.validate_python(workbench_url)).rstrip("/")
        except ValidationError:
            raise WorkbenchToolError(
                "tool_not_configured",
                "WORKBENCH_URL must be a valid HTTP or HTTPS URL.",
                details={"environment_variables": ["WORKBENCH_URL"]},
            ) from None

        try:
            credential = await self._sentinel.credential_for(
                sentinel_url=os.environ.get("SENTINEL_URL", "").strip() or DEFAULT_SENTINEL_URL,
                service_account_token=service_account_token,
                user_id=self._caller_user_id,
                application_client_id=workbench_client_id,
                run_id=run_id,
                session_id=session_id,
            )
        except SentinelClientError as error:
            if error.code == "sentinel_impersonation_denied":
                raise WorkbenchToolError(
                    "workbench_access_denied",
                    "The current user does not have access to Workbench.",
                ) from error
            if error.code == "sentinel_authentication_failed":
                raise WorkbenchToolError(
                    "tool_not_configured",
                    "Sentinel rejected the configured service-account credential.",
                    details={"environment_variables": ["SENTINEL_SERVICE_ACCOUNT_TOKEN"]},
                ) from error
            raise WorkbenchToolError(error.code, str(error), retryable=error.retryable) from error
        return WorkbenchSession(base_url=workbench_url, access_token=credential.access_token)

    async def query(
        self,
        workbench: WorkbenchSession,
        *,
        target_id: str,
        database_name: str | None,
        statement: str,
        read_only: bool,
    ) -> WorkbenchQueryResult:
        body: dict[str, JsonValue] = {"target_id": target_id, "statement": statement, "read_only": read_only}
        if database_name:
            body["database_name"] = database_name
        try:
            payload = await self.client.post(
                base_url=workbench.base_url,
                access_token=workbench.access_token,
                path="/queries",
                body=body,
            )
        except WorkbenchClientError as error:
            raise translate_workbench_error(error) from error
        try:
            return WorkbenchQueryResult.model_validate(payload)
        except ValidationError as error:
            raise WorkbenchToolError(
                "invalid_workbench_response", "Workbench returned an invalid query result."
            ) from error


def translate_workbench_error(error: WorkbenchClientError) -> WorkbenchToolError:
    if error.code == "workbench_access_denied":
        return WorkbenchToolError(
            error.code,
            "The current user is not a Workbench member, or lacks the Workbench role this action needs.",
        )
    if error.code == "workbench_read_only_violation":
        return WorkbenchToolError(
            error.code,
            "This statement writes data and was not run. Do not retry until the user has seen a read-only dry "
            "run and explicitly confirmed this exact statement in a later message; then use read_only=false. "
            "Workbench only allows writes for WorkbenchAdmins.",
        )
    return WorkbenchToolError(error.code, str(error), retryable=error.retryable)


def succeeded(call: ToolCall, output: JsonValue) -> ToolResult:
    return ToolResult(
        run_id=call.run_id,
        call_id=call.call_id,
        parent_session_id=call.parent_session_id,
        status=ToolResultStatus.SUCCEEDED,
        output=output,
    )


def failed(
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
        error=ContractError(code=code, message=message, retryable=retryable, details=details or {}),
    )


def failed_from(call: ToolCall, error: WorkbenchToolError) -> ToolResult:
    return failed(call, error.code, str(error), retryable=error.retryable, details=error.details)
