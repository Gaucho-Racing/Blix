import asyncio
import math
import os
import time
from typing import Annotated, Self

import httpx
from pydantic import BaseModel, ConfigDict, Field, JsonValue, SecretStr, ValidationError

from blix_agent_worker.models.common import JsonObject

DiscordID = Annotated[str, Field(strict=True, pattern=r"^[1-9][0-9]{0,19}$")]
ROOT_CHANNEL_TYPES = frozenset({0, 5, 15, 16})
PUBLIC_THREAD_TYPES = frozenset({10, 11})


class DiscordClientError(Exception):
    def __init__(self, code: str, message: str, *, retryable: bool = False, retry_after: float | None = None) -> None:
        self.code = code
        self.retryable = retryable
        self.retry_after = retry_after
        super().__init__(message)


class DiscordSettings(BaseModel):
    model_config = ConfigDict(frozen=True)

    bot_token: SecretStr
    guild_id: DiscordID

    @classmethod
    def from_environment(cls) -> Self:
        token = os.environ.get("DISCORD_BOT_TOKEN", "").strip()
        if not token or any(character.isspace() for character in token):
            raise DiscordClientError("tool_not_configured", "Set DISCORD_BOT_TOKEN to the raw bot token.")
        try:
            return cls(
                bot_token=SecretStr(token),
                guild_id=os.environ.get("DISCORD_GUILD_ID", "").strip(),
            )
        except ValidationError as error:
            raise DiscordClientError(
                "tool_not_configured",
                "Set DISCORD_GUILD_ID to the Discord server ID.",
            ) from error


class DiscordChannel(BaseModel):
    model_config = ConfigDict(extra="ignore")

    id: DiscordID
    guild_id: DiscordID
    type: int
    name: str
    topic: str | None = None
    parent_id: DiscordID | None = None
    last_message_id: DiscordID | None = None
    thread_metadata: JsonObject | None = None


class DiscordAuthor(BaseModel):
    model_config = ConfigDict(extra="ignore")

    id: DiscordID
    username: str
    global_name: str | None = None
    bot: bool = False


class DiscordAttachment(BaseModel):
    model_config = ConfigDict(extra="ignore")

    id: DiscordID
    filename: str
    size: int
    url: str
    content_type: str | None = None
    description: str | None = None


class DiscordMessage(BaseModel):
    model_config = ConfigDict(extra="ignore")

    id: DiscordID
    channel_id: DiscordID
    guild_id: DiscordID | None = None
    author: DiscordAuthor
    content: str
    timestamp: str
    edited_timestamp: str | None = None
    type: int
    pinned: bool = False
    attachments: tuple[DiscordAttachment, ...] = ()
    embeds: tuple[JsonObject, ...] = ()
    message_reference: JsonObject | None = None


class DiscordClient:
    def __init__(self, *, transport: httpx.AsyncBaseTransport | None = None) -> None:
        self._client = httpx.AsyncClient(
            base_url="https://discord.com/api/v10/",
            headers={
                "Accept": "application/json",
                "User-Agent": "DiscordBot (https://github.com/gaucho-racing/blix, 1)",
            },
            timeout=httpx.Timeout(20.0, connect=5.0),
            follow_redirects=False,
            transport=transport,
        )
        self._request_lock = asyncio.Lock()
        self._blocked_until = 0.0

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self._client.aclose()

    async def channels(self, settings: DiscordSettings) -> tuple[DiscordChannel, ...]:
        body = await self._get(f"guilds/{settings.guild_id}/channels", settings)
        channels = self._channels(body)
        return tuple(channel for channel in channels if self._allowed_root(channel, settings))

    async def channel(self, channel_id: str, settings: DiscordSettings) -> DiscordChannel:
        body = await self._get(f"channels/{channel_id}", settings)
        if not isinstance(body, dict):
            raise self._invalid_response()
        if body.get("id") != channel_id or body.get("guild_id") != settings.guild_id:
            raise self._denied()
        channel = DiscordChannel.model_validate(body)
        if self._allowed_root(channel, settings):
            return channel
        if channel.type in PUBLIC_THREAD_TYPES and channel.parent_id is not None:
            parent = DiscordChannel.model_validate(await self._get(f"channels/{channel.parent_id}", settings))
            if parent.id == channel.parent_id and self._allowed_root(parent, settings):
                return channel
        raise self._denied()

    async def messages(
        self, channel: DiscordChannel, settings: DiscordSettings, query: dict[str, str | int]
    ) -> tuple[DiscordMessage, ...]:
        body = await self._get(f"channels/{channel.id}/messages", settings, query)
        if not isinstance(body, list):
            raise self._invalid_response()
        messages = tuple(DiscordMessage.model_validate(item) for item in body)
        for message in messages:
            self._check_message(message, channel, settings)
        return tuple(sorted(messages, key=lambda message: int(message.id), reverse=True))

    async def message(self, channel: DiscordChannel, message_id: str, settings: DiscordSettings) -> DiscordMessage:
        message = DiscordMessage.model_validate(
            await self._get(f"channels/{channel.id}/messages/{message_id}", settings)
        )
        self._check_message(message, channel, settings)
        if message.id != message_id:
            raise self._invalid_response()
        return message

    async def active_threads(self, settings: DiscordSettings) -> tuple[DiscordChannel, ...]:
        parents = {channel.id for channel in await self.channels(settings)}
        body = await self._get(f"guilds/{settings.guild_id}/threads/active", settings)
        if not isinstance(body, dict):
            raise self._invalid_response()
        threads = self._channels(body.get("threads"))
        return tuple(
            sorted(
                (
                    thread
                    for thread in threads
                    if thread.guild_id == settings.guild_id
                    and thread.type in PUBLIC_THREAD_TYPES
                    and thread.parent_id in parents
                ),
                key=lambda thread: int(thread.id),
                reverse=True,
            )
        )

    async def archived_threads(
        self, channel: DiscordChannel, settings: DiscordSettings, query: dict[str, str | int]
    ) -> tuple[tuple[DiscordChannel, ...], bool]:
        if channel.type not in ROOT_CHANNEL_TYPES:
            raise DiscordClientError(
                "invalid_arguments", "List archived threads on their parent channel, not a thread."
            )
        body = await self._get(f"channels/{channel.id}/threads/archived/public", settings, query)
        if not isinstance(body, dict) or not isinstance(body.get("has_more"), bool):
            raise self._invalid_response()
        threads = self._channels(body.get("threads"))
        if any(
            thread.guild_id != settings.guild_id
            or thread.type not in PUBLIC_THREAD_TYPES
            or thread.parent_id != channel.id
            for thread in threads
        ):
            raise self._invalid_response()
        return threads, body["has_more"]

    async def _get(self, path: str, settings: DiscordSettings, query: dict[str, str | int] | None = None) -> JsonValue:
        async with self._request_lock:
            remaining = self._blocked_until - time.monotonic()
            if remaining > 0:
                raise DiscordClientError(
                    "discord_rate_limited",
                    "Discord reads are cooling down; retry later.",
                    retryable=True,
                    retry_after=remaining,
                )
            try:
                response = await self._client.get(
                    path, params=query, headers={"Authorization": f"Bot {settings.bot_token.get_secret_value()}"}
                )
            except httpx.RequestError as error:
                raise DiscordClientError(
                    "discord_unavailable", "Discord could not be reached.", retryable=True
                ) from error
            if response.status_code == 429:
                try:
                    delay = response.json().get("retry_after", response.headers.get("Retry-After", 1))
                except (ValueError, AttributeError):
                    delay = response.headers.get("Retry-After", 1)
                delay = self._retry_delay(delay)
                self._blocked_until = time.monotonic() + delay
                raise DiscordClientError(
                    "discord_rate_limited",
                    "Discord rate limited the request; retry after the supplied delay.",
                    retryable=True,
                    retry_after=delay,
                )
            if response.headers.get("X-RateLimit-Remaining") == "0":
                self._blocked_until = time.monotonic() + self._retry_delay(
                    response.headers.get("X-RateLimit-Reset-After", 1)
                )
            if response.status_code == 401:
                raise DiscordClientError("tool_not_configured", "Discord rejected DISCORD_BOT_TOKEN.")
            if response.status_code == 403:
                raise DiscordClientError(
                    "discord_access_denied", "The bot lacks access. Check View Channel and Read Message History."
                )
            if response.status_code == 404:
                raise DiscordClientError(
                    "discord_not_found", "Discord could not find the requested channel or message."
                )
            if not response.is_success:
                raise DiscordClientError(
                    "discord_request_failed",
                    f"Discord returned HTTP {response.status_code}.",
                    retryable=response.status_code >= 500,
                )
            try:
                return response.json()
            except ValueError as error:
                raise self._invalid_response() from error

    @staticmethod
    def _retry_delay(value: object) -> float:
        try:
            delay = float(value)
        except (ValueError, TypeError):
            return 1.0
        return max(delay, 0.1) if math.isfinite(delay) else 1.0

    @staticmethod
    def _channels(body: JsonValue) -> tuple[DiscordChannel, ...]:
        if not isinstance(body, list):
            raise DiscordClient._invalid_response()
        return tuple(DiscordChannel.model_validate(item) for item in body)

    @staticmethod
    def _allowed_root(channel: DiscordChannel, settings: DiscordSettings) -> bool:
        return channel.guild_id == settings.guild_id and channel.type in ROOT_CHANNEL_TYPES

    @staticmethod
    def _check_message(message: DiscordMessage, channel: DiscordChannel, settings: DiscordSettings) -> None:
        if message.channel_id != channel.id or message.guild_id not in {None, settings.guild_id}:
            raise DiscordClient._invalid_response()

    @staticmethod
    def _denied() -> DiscordClientError:
        return DiscordClientError(
            "discord_channel_not_allowed",
            "Only supported channels in the configured server and their public threads are available; "
            "DMs and private threads are excluded.",
        )

    @staticmethod
    def _invalid_response() -> DiscordClientError:
        return DiscordClientError("invalid_discord_response", "Discord returned an invalid response.")
