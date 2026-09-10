import os
from datetime import datetime
from typing import Literal

from pydantic import HttpUrl, TypeAdapter, ValidationError

from blix_agent_worker.clients import SentinelClient, SentinelClientError
from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    JsonObject,
    NonEmptyText,
    validation_error_details,
)
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact, ToolResult, ToolResultStatus

_HTTP_URL_VALIDATOR = TypeAdapter(HttpUrl)
DEFAULT_SENTINEL_URL = "https://sso.gauchoracing.com"


class SentinelImpersonateV1Input(ContractModel):
    user_id: NonEmptyText
    application_id: NonEmptyText


class SentinelImpersonateV1Output(ContractModel):
    token_id: NonEmptyText
    user_id: NonEmptyText
    application_id: NonEmptyText
    token_type: Literal["Bearer"]
    scope: NonEmptyText
    expires_at: datetime


class SentinelImpersonateV1Tool:
    definition = ToolDefinition(
        id="sentinel_impersonate_v1",
        description=(
            "Create a short-lived Sentinel credential that acts as a user for a target application. "
            "The access token remains private; this returns its non-secret reference and metadata."
        ),
        input_schema=SentinelImpersonateV1Input.model_json_schema(mode="validation"),
        output_schema=SentinelImpersonateV1Output.model_json_schema(mode="serialization"),
        impact=ToolImpact.MUTATING,
    )

    def __init__(self, client: SentinelClient) -> None:
        self._client = client

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = SentinelImpersonateV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return self._failure(
                call,
                "invalid_arguments",
                "The Sentinel impersonation arguments are invalid.",
                details=validation_error_details(error),
            )

        sentinel_url = os.environ.get("SENTINEL_URL", "").strip() or DEFAULT_SENTINEL_URL
        service_account_token = os.environ.get("SENTINEL_SERVICE_ACCOUNT_TOKEN", "").strip()
        missing = [
            name
            for name, value in {
                "SENTINEL_SERVICE_ACCOUNT_TOKEN": service_account_token,
            }.items()
            if not value
        ]
        if missing:
            return self._failure(
                call,
                "tool_not_configured",
                f"Tool {self.definition.id!r} is missing required configuration.",
                details={"environment_variables": missing},
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
            credential = await self._client.impersonate(
                sentinel_url=sentinel_url,
                service_account_token=service_account_token,
                user_id=tool_input.user_id,
                application_id=tool_input.application_id,
                run_id=call.run_id,
                execution_id=call.parent_execution_id,
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

        output = SentinelImpersonateV1Output(
            token_id=credential.token_id,
            user_id=credential.user_id,
            application_id=credential.application_id,
            token_type=credential.token_type,
            scope=credential.scope,
            expires_at=credential.expires_at,
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
