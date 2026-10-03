import os
from typing import Annotated, Literal, Self

from pydantic import Field, HttpUrl, TypeAdapter, ValidationError, model_validator

from blix_agent_worker.clients.sentinel import SentinelClient, SentinelClientError
from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    EntityID,
    ServiceAccountID,
    UserID,
    validation_error_details,
)
from blix_agent_worker.models.context import EntityType
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolResult, ToolResultStatus


class SentinelIdentityResolveV1Input(ContractModel):
    provider: Literal["discord"] | None = Field(default=None, description="Use with external_id for Discord lookup.")
    external_id: Annotated[str, Field(strict=True, pattern=r"^[1-9][0-9]{0,19}$")] | None = Field(
        default=None,
        description="Exact Discord user ID, not a display name, username, mention, or channel ID."
    )
    entity_id: EntityID | None = Field(default=None, description="Sentinel entity ID to look up its linked provider IDs.")

    @model_validator(mode="after")
    def validate_lookup(self) -> Self:
        if self.entity_id is not None:
            if self.provider is not None or self.external_id is not None:
                raise ValueError("Use entity_id alone, or provider and external_id together")
        elif self.provider is None or self.external_id is None:
            raise ValueError("Use entity_id alone, or provider and external_id together")
        return self


class LinkedProviderID(ContractModel):
    provider: str
    external_id: str


class SentinelIdentityResolveV1Output(ContractModel):
    found: bool
    entity_id: EntityID | None = None
    entity_type: EntityType | None = None
    user_id: UserID | None = None
    service_account_id: ServiceAccountID | None = None
    linked_ids: tuple[LinkedProviderID, ...] = ()
    full_name: str | None = None


class SentinelIdentityResolveV1Tool:
    definition = ToolDefinition(
        id="sentinel_identity_resolve_v1",
        description=(
            "Resolve either a Sentinel entity_id alone or an exact Discord ID using provider=discord and external_id. "
            "Returns the Sentinel entity/user IDs, name, and all linked provider IDs. "
            "For a named person, first find their entity_id with sentinel_api_v1 GET /api/users, then use this tool "
            "to find their linked Discord ID. Do not guess identity matches or provider IDs. "
            "Uses Blix's Sentinel service account with user:read, not caller impersonation. Returns identifiers "
            "and safe identity metadata, never credentials or provider tokens. found=false means the entity was not found. "
            "Use sentinel_api_v1 GET /api/users/{user_id} for the full profile using the returned user_id. "
            "Lookup does not authenticate the Discord author, authorize actions, or change the current caller. "
            "Google/email/GitHub lookups are not supported yet."
        ),
        input_schema=SentinelIdentityResolveV1Input.model_json_schema(mode="validation"),
        output_schema=SentinelIdentityResolveV1Output.model_json_schema(mode="serialization"),
        idempotent=True,
    )

    def __init__(self, client: SentinelClient) -> None:
        self._client = client

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            request = SentinelIdentityResolveV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return self._failure(
                call,
                ContractError(
                    code="invalid_arguments",
                    message="Invalid identity lookup arguments.",
                    details=validation_error_details(error),
                ),
            )
        token = os.environ.get("SENTINEL_SERVICE_ACCOUNT_TOKEN", "").strip()
        if not token:
            return self._failure(
                call,
                ContractError(
                    code="tool_not_configured",
                    message="Set SENTINEL_SERVICE_ACCOUNT_TOKEN with user:read access.",
                ),
            )
        try:
            url = TypeAdapter(HttpUrl).validate_python(
                os.environ.get("SENTINEL_URL", "").strip() or "https://sso.gauchoracing.com"
            )
            if url.username or url.password or url.query or url.fragment:
                raise ValueError("URL must not include credentials, query, or fragment")
        except ValueError:
            return self._failure(
                call,
                ContractError(
                    code="tool_not_configured",
                    message="SENTINEL_URL must be an HTTP(S) base URL without credentials or query.",
                ),
            )
        try:
            if request.entity_id is not None:
                identity = await self._client.resolve_entity_identity(
                    sentinel_url=str(url).rstrip("/"),
                    service_account_token=token,
                    entity_id=request.entity_id,
                )
            else:
                identity = await self._client.resolve_external_identity(
                    sentinel_url=str(url).rstrip("/"),
                    service_account_token=token,
                    provider=request.provider,
                    external_id=request.external_id,
                )
            canonical = None
            if request.entity_id is None and identity is not None and identity.type is EntityType.USER:
                canonical = await self._client.resolve_external_user(
                    sentinel_url=str(url).rstrip("/"),
                    service_account_token=token,
                    provider=request.provider,
                    external_id=request.external_id,
                )
        except SentinelClientError as error:
            return self._failure(
                call,
                ContractError(
                    code=error.code,
                    message=str(error),
                    retryable=error.retryable,
                ),
            )
        output = SentinelIdentityResolveV1Output(found=False)
        if identity is not None:
            full_name = canonical.full_name if canonical else None
            if full_name is None and identity.user is not None:
                full_name = " ".join(
                    part.strip() for part in (identity.user.first_name, identity.user.last_name) if part.strip()
                ) or None
            output = SentinelIdentityResolveV1Output(
                found=True,
                entity_id=identity.id,
                entity_type=identity.type,
                user_id=identity.user.id if identity.user else None,
                service_account_id=identity.service_account.id if identity.service_account else None,
                linked_ids=tuple(
                    LinkedProviderID(provider=item.provider.lower(), external_id=item.external_id)
                    for item in identity.external_auths
                ),
                full_name=full_name,
            )
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_session_id=call.parent_session_id,
            status=ToolResultStatus.SUCCEEDED,
            output=output.model_dump(mode="json"),
        )

    @staticmethod
    def _failure(call: ToolCall, error: ContractError) -> ToolResult:
        return ToolResult(
            run_id=call.run_id,
            call_id=call.call_id,
            parent_session_id=call.parent_session_id,
            status=ToolResultStatus.FAILED,
            error=error,
        )
