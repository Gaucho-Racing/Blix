import os
import re

from pydantic import Field, HttpUrl, JsonValue, TypeAdapter, ValidationError, field_validator

from blix_agent_worker.clients import SentinelClient, SentinelClientError
from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    JsonObject,
    NonEmptyText,
    UserID,
    validation_error_details,
)
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult, ToolResultStatus

type SentinelQueryScalar = str | int | float | bool
type SentinelQueryValue = SentinelQueryScalar | tuple[SentinelQueryScalar, ...]

_HTTP_URL_VALIDATOR = TypeAdapter(HttpUrl)
_PATH_SEGMENT = r"[A-Za-z0-9][A-Za-z0-9._:@+-]*"
_READ_PATHS = tuple(
    re.compile(pattern)
    for pattern in (
        r"/api/users",
        r"/api/users/check-username",
        rf"/api/users/{_PATH_SEGMENT}",
        rf"/api/users/{_PATH_SEGMENT}/(?:groups|logins|recent-applications)",
        r"/api/groups",
        rf"/api/groups/{_PATH_SEGMENT}",
        rf"/api/groups/{_PATH_SEGMENT}/(?:write-access|applications|members|conditional-bindings|owners|requests)",
        rf"/api/groups/{_PATH_SEGMENT}/requests/{_PATH_SEGMENT}",
        r"/api/applications",
        rf"/api/applications/{_PATH_SEGMENT}",
        rf"/api/applications/client/{_PATH_SEGMENT}",
        rf"/api/applications/{_PATH_SEGMENT}/(?:write-access|groups|service-accounts|redirect-uris)",
    )
)
DEFAULT_SENTINEL_URL = "https://sso.gauchoracing.com"
SENTINEL_CLIENT_ID = "sentinel"


class SentinelApiV1Input(ContractModel):
    path: NonEmptyText = Field(
        description=(
            "Sentinel gateway path beginning with /api/users, /api/groups, or /api/applications. "
            "Supported reads include user groups/logins/recent-applications; group members, owners, applications, "
            "conditional bindings, and join requests; and application groups, service accounts, and redirect URIs."
        )
    )
    query: dict[str, SentinelQueryValue] = Field(default_factory=dict)

    @field_validator("path")
    @classmethod
    def path_must_be_supported(cls, path: str) -> str:
        if any(character in path for character in ("?", "#", "\\", "%")) or "//" in path:
            raise ValueError("path must be an unencoded URL path without a query string or fragment")
        if not any(pattern.fullmatch(path) for pattern in _READ_PATHS):
            raise ValueError("path is not a supported Sentinel read endpoint")
        return path

    @field_validator("query")
    @classmethod
    def query_keys_must_be_valid(cls, query: dict[str, SentinelQueryValue]) -> dict[str, SentinelQueryValue]:
        if any(not key or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", key) for key in query):
            raise ValueError("query parameter names contain unsupported characters")
        return query


class SentinelApiV1Output(ContractModel):
    path: NonEmptyText
    status_code: int
    data: JsonValue


class SentinelApiV1Tool:
    definition = ToolDefinition(
        id="sentinel_api_v1",
        description=(
            "Read Sentinel users, groups, and applications as the current Blix caller. The tool automatically uses "
            "a short-lived impersonation credential and never exposes it. Pass query parameters separately from path."
        ),
        input_schema=SentinelApiV1Input.model_json_schema(mode="validation"),
        output_schema=SentinelApiV1Output.model_json_schema(mode="serialization"),
        idempotent=True,
    )

    def __init__(self, client: SentinelClient, caller_user_id: UserID) -> None:
        self._client = client
        self._caller_user_id = caller_user_id

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = SentinelApiV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return self._failure(
                call,
                "invalid_arguments",
                "The Sentinel API arguments are invalid.",
                details=validation_error_details(error),
            )

        sentinel_url = os.environ.get("SENTINEL_URL", "").strip() or DEFAULT_SENTINEL_URL
        service_account_token = os.environ.get("SENTINEL_SERVICE_ACCOUNT_TOKEN", "").strip()
        if not service_account_token:
            return self._failure(
                call,
                "tool_not_configured",
                f"Tool {self.definition.id!r} requires SENTINEL_SERVICE_ACCOUNT_TOKEN.",
                details={"environment_variables": ["SENTINEL_SERVICE_ACCOUNT_TOKEN"]},
            )
        try:
            sentinel_url = str(_HTTP_URL_VALIDATOR.validate_python(sentinel_url)).rstrip("/")
        except ValidationError:
            return self._failure(
                call,
                "tool_not_configured",
                "SENTINEL_URL must be a valid HTTP or HTTPS URL.",
                details={"environment_variables": ["SENTINEL_URL"]},
            )

        try:
            credential = await self._client.credential_for(
                sentinel_url=sentinel_url,
                service_account_token=service_account_token,
                user_id=self._caller_user_id,
                application_client_id=SENTINEL_CLIENT_ID,
                run_id=call.run_id,
                execution_id=call.parent_execution_id,
            )
            status_code, data = await self._client.get_api(
                sentinel_url=sentinel_url,
                credential=credential,
                path=tool_input.path,
                query=tool_input.query,
            )
        except SentinelClientError as error:
            if error.code == "sentinel_authentication_failed":
                return self._failure(
                    call,
                    "tool_not_configured",
                    "Sentinel rejected the configured service-account credential.",
                    details={"environment_variables": ["SENTINEL_SERVICE_ACCOUNT_TOKEN"]},
                )
            return self._failure(call, error.code, str(error), retryable=error.retryable)

        output = SentinelApiV1Output(path=tool_input.path, status_code=status_code, data=data)
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
