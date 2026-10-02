from pydantic import ConfigDict, Field, HttpUrl, TypeAdapter, ValidationError, field_validator

from blix_agent_worker.clients.discord import DiscordClient, DiscordClientError, DiscordID, DiscordSettings
from blix_agent_worker.clients.sentinel import SentinelUser
from blix_agent_worker.models.common import ContractError, ContractModel, validation_error_details
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact, ToolResult, ToolResultStatus


class DiscordWebhookSendV1Input(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)

    channel_id: DiscordID = Field(description="Destination text/announcement channel or existing public thread ID.")
    content: str = Field(
        min_length=1,
        max_length=2000,
        description="Message body to publish. Mentions are enabled. Runtime adds the divider and Blix attribution.",
    )

    @field_validator("content")
    @classmethod
    def require_content(cls, value: str) -> str:
        if not value.strip():
            raise ValueError("content must not be blank")
        return value


class DiscordWebhookSendV1Output(ContractModel):
    guild_id: DiscordID
    channel_id: DiscordID
    message_id: DiscordID
    url: str
    username: str
    webhook_deleted: bool


class DiscordWebhookSendV1Tool:
    definition = ToolDefinition(
        id="discord_webhook_send_v1",
        description=(
            "Publish a Discord message only when the user requests sending it. Creates a temporary channel webhook, "
            "sends once, then attempts deletion. Runtime supplies the verified caller's name and avatar, and adds "
            "a divider with a small 'Sent via Blix' footer. Do not add attribution yourself. This is a bot/webhook "
            "message, not the user's Discord account. Bot permissions and the "
            "configured guild apply. User, role, and everyone mentions are enabled. No attachments or new threads. "
            "An uncertain result must not be blindly retried. Successful sends return a message URL; failed cleanup "
            "does not mean the message failed."
        ),
        input_schema=DiscordWebhookSendV1Input.model_json_schema(mode="validation"),
        output_schema=DiscordWebhookSendV1Output.model_json_schema(mode="serialization"),
        impact=ToolImpact.MUTATING,
        idempotent=False,
    )

    def __init__(self, client: DiscordClient, caller: SentinelUser) -> None:
        self._client = client
        self._caller = caller

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            request = DiscordWebhookSendV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return self._failure(
                call,
                ContractError(
                    code="invalid_arguments",
                    message="Invalid Discord webhook arguments.",
                    details=validation_error_details(error),
                ),
            )
        name = " ".join(f"{self._caller.first_name} {self._caller.last_name}".split())
        name = name or self._caller.username.strip() or "Member"
        username = name[:80]
        avatar_url = None
        if self._caller.avatar_url:
            try:
                avatar_url = str(TypeAdapter(HttpUrl).validate_python(self._caller.avatar_url))
            except ValidationError:
                pass
        try:
            result = await self._client.webhook_send(
                DiscordSettings.from_environment(),
                channel_id=request.channel_id,
                content=request.content,
                username=username,
                avatar_url=avatar_url,
                audit_reason=f"Blix caller={self._caller.entity_id} run={call.run_id} call={call.call_id}",
            )
            output = DiscordWebhookSendV1Output.model_validate(result)
        except DiscordClientError as error:
            return self._failure(
                call,
                ContractError(
                    code=error.code,
                    message=str(error),
                    retryable=error.retryable,
                    details={} if error.retry_after is None else {"retry_after_seconds": error.retry_after},
                ),
            )
        except ValidationError:
            return self._failure(
                call,
                ContractError(
                    code="invalid_discord_response",
                    message="Discord returned an invalid webhook response.",
                ),
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
