import asyncio
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from typing import Literal, Self
from urllib.parse import quote

import httpx
from pydantic import BaseModel, ConfigDict, Field, JsonValue, SecretStr, model_validator

from blix_agent_worker.models.common import RunID, EntityID, ServiceAccountID, SessionID, UserID
from blix_agent_worker.models.context import EntityType


class SentinelClientError(Exception):
    def __init__(self, code: str, message: str, *, retryable: bool = False) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class SentinelCredentialNotFoundError(Exception):
    pass


class _ImpersonationResponse(BaseModel):
    model_config = ConfigDict(extra="forbid")

    access_token: SecretStr
    token_id: str = Field(min_length=1)
    token_type: Literal["Bearer"]
    expires_in: int = Field(gt=0)
    scope: str = Field(min_length=1)


class _ApplicationResponse(BaseModel):
    model_config = ConfigDict(extra="ignore")

    id: str = Field(min_length=1)
    client_id: str = Field(min_length=1)


class SentinelUser(BaseModel):
    model_config = ConfigDict(extra="allow", frozen=True)

    id: UserID
    entity_id: EntityID
    username: str
    first_name: str
    last_name: str
    email: str
    phone_number: str
    gender: str
    birthday: datetime
    graduate_level: str
    graduation_year: int
    major: str
    shirt_size: str
    jacket_size: str
    sae_registration_number: str
    occupation_title: str
    occupation_company: str
    avatar_url: str
    initial_role: str
    groups: tuple[str, ...]
    updated_at: datetime
    created_at: datetime


class _SentinelServiceAccount(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    id: ServiceAccountID
    entity_id: EntityID


class SentinelLinkedAccount(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    entity_id: EntityID
    provider: str = Field(min_length=1)
    external_id: str = Field(min_length=1)


class _LinkedUser(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    id: UserID
    entity_id: EntityID


class SentinelLinkedIdentity(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    id: EntityID
    type: EntityType
    user: _LinkedUser | None = None
    service_account: _SentinelServiceAccount | None = None
    external_auths: tuple[SentinelLinkedAccount, ...]

    @model_validator(mode="after")
    def validate_links(self) -> Self:
        profile = self.user if self.type is EntityType.USER else self.service_account
        if profile is None or profile.entity_id != self.id:
            raise ValueError("Sentinel returned a mismatched identity")
        if self.user is not None and self.service_account is not None:
            raise ValueError("Sentinel returned multiple identity types")
        if any(account.entity_id != self.id for account in self.external_auths):
            raise ValueError("Sentinel returned a mismatched linked account")
        return self


class SentinelCanonicalUser(BaseModel):
    model_config = ConfigDict(frozen=True)

    entity_id: EntityID
    user_id: UserID
    full_name: str = Field(min_length=1)


class SentinelEntity(BaseModel):
    model_config = ConfigDict(extra="ignore", frozen=True)

    id: EntityID
    type: EntityType
    user: SentinelUser | None = None
    service_account: _SentinelServiceAccount | None = None

    @model_validator(mode="after")
    def validate_identity(self) -> Self:
        if self.type is EntityType.USER:
            if self.user is None or self.service_account is not None or self.user.entity_id != self.id:
                raise ValueError("Sentinel returned an invalid user entity")
        elif self.service_account is None or self.user is not None or self.service_account.entity_id != self.id:
            raise ValueError("Sentinel returned an invalid service-account entity")
        return self


@dataclass(frozen=True, slots=True)
class SentinelCredential:
    token_id: str
    user_id: str
    application_id: str
    run_id: RunID
    session_id: SessionID
    token_type: Literal["Bearer"]
    scope: str
    expires_at: datetime
    access_token: str = field(repr=False)


class SentinelClient:
    def __init__(self, *, transport: httpx.AsyncBaseTransport | None = None) -> None:
        self._client = httpx.AsyncClient(
            headers={"Accept": "application/json", "Content-Type": "application/json"},
            timeout=30,
            transport=transport,
        )
        self._credentials: dict[str, SentinelCredential] = {}
        self._application_ids: dict[tuple[str, str], str] = {}
        self._canonical_users: dict[tuple[str, str, str], SentinelCanonicalUser | None] = {}
        self._canonical_user_locks: dict[tuple[str, str, str], asyncio.Lock] = {}

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        self._credentials.clear()
        self._application_ids.clear()
        self._canonical_users.clear()
        self._canonical_user_locks.clear()
        await self._client.aclose()

    async def get_entity(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        entity_id: EntityID,
    ) -> SentinelEntity:
        try:
            response = await self._client.get(
                f"{sentinel_url.rstrip('/')}/api/core/entity/{entity_id}",
                headers={"Authorization": f"Bearer {service_account_token}"},
            )
        except httpx.RequestError as error:
            raise SentinelClientError(
                "sentinel_unavailable",
                "Sentinel is unavailable while resolving the caller.",
                retryable=True,
            ) from error

        if response.is_error:
            raise self._entity_response_error(response)

        try:
            return SentinelEntity.model_validate(response.json())
        except ValueError as error:
            raise SentinelClientError(
                "invalid_sentinel_response",
                "Sentinel returned an invalid caller entity.",
            ) from error

    async def resolve_external_identity(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        provider: str,
        external_id: str,
    ) -> SentinelLinkedIdentity | None:
        try:
            response = await self._client.get(
                f"{sentinel_url.rstrip('/')}/api/core/entity/external/"
                f"{quote(provider.upper(), safe='')}/{quote(external_id, safe='')}",
                headers={"Authorization": f"Bearer {service_account_token}"},
            )
        except httpx.RequestError:
            raise SentinelClientError(
                "sentinel_unavailable",
                "Sentinel is unavailable while resolving linked identities.",
                retryable=True,
            ) from None
        if response.status_code == 404:
            return None
        if response.status_code == 401:
            raise SentinelClientError("tool_not_configured", "Sentinel rejected SENTINEL_SERVICE_ACCOUNT_TOKEN.")
        if response.status_code == 403:
            raise SentinelClientError(
                "sentinel_identity_read_denied",
                "Sentinel denied linked-identity lookup. The service account needs user:read and "
                "Sentinel must have the scoped lookup endpoint deployed.",
            )
        if not response.is_success:
            raise SentinelClientError(
                "sentinel_identity_request_failed",
                f"Sentinel returned HTTP {response.status_code}.",
                retryable=response.status_code == 429 or response.status_code >= 500,
            )
        try:
            identity = SentinelLinkedIdentity.model_validate(response.json())
            if not any(
                account.provider.casefold() == provider.casefold() and account.external_id == external_id
                for account in identity.external_auths
            ):
                raise ValueError("Sentinel returned an unrelated identity")
            return identity
        except ValueError:
            raise SentinelClientError(
                "invalid_sentinel_response",
                "Sentinel returned an invalid linked identity.",
            ) from None

    async def resolve_external_user(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        provider: str,
        external_id: str,
    ) -> SentinelCanonicalUser | None:
        cache_key = (sentinel_url.rstrip("/"), provider.casefold(), external_id)
        if cache_key in self._canonical_users:
            return self._canonical_users[cache_key]
        lock = self._canonical_user_locks.setdefault(cache_key, asyncio.Lock())
        async with lock:
            if cache_key in self._canonical_users:
                return self._canonical_users[cache_key]
            user = await self._resolve_external_user(
                sentinel_url=sentinel_url,
                service_account_token=service_account_token,
                provider=provider,
                external_id=external_id,
            )
            self._canonical_users[cache_key] = user
            return user

    async def _resolve_external_user(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        provider: str,
        external_id: str,
    ) -> SentinelCanonicalUser | None:
        identity = await self.resolve_external_identity(
            sentinel_url=sentinel_url,
            service_account_token=service_account_token,
            provider=provider,
            external_id=external_id,
        )
        if identity is None:
            return None
        if identity.type is not EntityType.USER or identity.user is None:
            raise SentinelClientError("sentinel_identity_not_user", "The linked identity is not a Sentinel user.")
        entity = await self.get_entity(
            sentinel_url=sentinel_url,
            service_account_token=service_account_token,
            entity_id=identity.id,
        )
        if entity.user is None or entity.user.id != identity.user.id:
            raise SentinelClientError("invalid_sentinel_response", "Sentinel returned a mismatched user profile.")
        full_name = " ".join(part.strip() for part in (entity.user.first_name, entity.user.last_name) if part.strip())
        if not full_name:
            raise SentinelClientError("sentinel_name_unavailable", "The Sentinel user has no canonical full name.")
        return SentinelCanonicalUser(entity_id=identity.id, user_id=identity.user.id, full_name=full_name)

    async def impersonate(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        user_id: str,
        application_id: str,
        run_id: RunID,
        session_id: SessionID,
    ) -> SentinelCredential:
        requested_at = datetime.now(UTC)
        try:
            response = await self._client.post(
                f"{sentinel_url.rstrip('/')}/api/core/token/impersonate",
                headers={"Authorization": f"Bearer {service_account_token}"},
                json={"user_id": user_id, "application_id": application_id},
            )
        except httpx.RequestError as error:
            raise SentinelClientError(
                "sentinel_unavailable",
                "Sentinel is unavailable.",
                retryable=True,
            ) from error

        if response.is_error:
            raise self._response_error(response)

        try:
            payload = _ImpersonationResponse.model_validate(response.json())
        except ValueError as error:
            raise SentinelClientError(
                "invalid_sentinel_response",
                "Sentinel returned an invalid impersonation response.",
            ) from error

        credential = SentinelCredential(
            token_id=payload.token_id,
            access_token=payload.access_token.get_secret_value(),
            user_id=user_id,
            application_id=application_id,
            run_id=run_id,
            session_id=session_id,
            token_type=payload.token_type,
            scope=payload.scope,
            expires_at=requested_at + timedelta(seconds=payload.expires_in),
        )
        self._remove_expired_credentials()
        self._credentials[credential.token_id] = credential
        return credential

    async def credential_for(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        user_id: str,
        application_client_id: str,
        run_id: RunID,
        session_id: SessionID,
    ) -> SentinelCredential:
        application_id = await self._application_id_for_client(
            sentinel_url=sentinel_url,
            service_account_token=service_account_token,
            client_id=application_client_id,
        )
        self._remove_expired_credentials()
        credential = next(
            (
                candidate
                for candidate in self._credentials.values()
                if candidate.user_id == user_id
                and candidate.application_id == application_id
                and candidate.run_id == run_id
                and candidate.session_id == session_id
            ),
            None,
        )
        if credential is not None:
            return credential
        return await self.impersonate(
            sentinel_url=sentinel_url,
            service_account_token=service_account_token,
            user_id=user_id,
            application_id=application_id,
            run_id=run_id,
            session_id=session_id,
        )

    async def get_api(
        self,
        *,
        sentinel_url: str,
        credential: SentinelCredential,
        path: str,
        query: dict[str, str | int | float | bool | tuple[str | int | float | bool, ...]],
    ) -> tuple[int, JsonValue]:
        try:
            response = await self._client.get(
                f"{sentinel_url.rstrip('/')}{path}",
                headers={"Authorization": f"Bearer {credential.access_token}"},
                params=query,
            )
        except httpx.RequestError as error:
            raise SentinelClientError(
                "sentinel_api_unavailable",
                "Sentinel is unavailable.",
                retryable=True,
            ) from error

        if response.is_error:
            raise self._api_response_error(response)
        if not response.content:
            return response.status_code, None
        try:
            return response.status_code, response.json()
        except ValueError as error:
            raise SentinelClientError(
                "invalid_sentinel_response",
                "Sentinel returned a non-JSON API response.",
            ) from error

    def get_credential(
        self, token_id: str, *, run_id: RunID, session_id: SessionID
    ) -> SentinelCredential:
        credential = self._credentials.get(token_id)
        if credential is None:
            raise SentinelCredentialNotFoundError(token_id)
        if credential.expires_at <= datetime.now(UTC):
            self._credentials.pop(token_id, None)
            raise SentinelCredentialNotFoundError(token_id)
        if credential.run_id != run_id or credential.session_id != session_id:
            raise SentinelCredentialNotFoundError(token_id)
        return credential

    async def _application_id_for_client(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        client_id: str,
    ) -> str:
        cache_key = (sentinel_url.rstrip("/"), client_id)
        application_id = self._application_ids.get(cache_key)
        if application_id is not None:
            return application_id
        try:
            response = await self._client.get(
                f"{cache_key[0]}/api/applications/client/{quote(client_id, safe='')}",
                headers={"Authorization": f"Bearer {service_account_token}"},
            )
        except httpx.RequestError as error:
            raise SentinelClientError(
                "sentinel_unavailable",
                "Sentinel is unavailable while resolving the target application.",
                retryable=True,
            ) from error
        if response.is_error:
            raise self._application_response_error(response)
        try:
            application = _ApplicationResponse.model_validate(response.json())
        except ValueError as error:
            raise SentinelClientError(
                "invalid_sentinel_response",
                "Sentinel returned an invalid application response.",
            ) from error
        if application.client_id != client_id:
            raise SentinelClientError(
                "invalid_sentinel_response",
                "Sentinel returned a mismatched application.",
            )
        self._application_ids[cache_key] = application.id
        return application.id

    def _remove_expired_credentials(self) -> None:
        now = datetime.now(UTC)
        expired = [token_id for token_id, credential in self._credentials.items() if credential.expires_at <= now]
        for token_id in expired:
            del self._credentials[token_id]

    @classmethod
    def _response_error(cls, response: httpx.Response) -> SentinelClientError:
        message = cls._error_message(response)
        if response.status_code == 401:
            return SentinelClientError("sentinel_authentication_failed", message)
        if response.status_code == 403:
            return SentinelClientError("sentinel_impersonation_denied", message)
        if response.status_code == 404:
            return SentinelClientError("sentinel_resource_not_found", message)
        return SentinelClientError(
            "sentinel_request_failed",
            message,
            retryable=response.status_code == 429 or response.status_code >= 500,
        )

    @classmethod
    def _entity_response_error(cls, response: httpx.Response) -> SentinelClientError:
        message = cls._error_message(response)
        if response.status_code == 401:
            return SentinelClientError("sentinel_authentication_failed", message)
        if response.status_code == 403:
            return SentinelClientError("sentinel_entity_read_denied", message)
        if response.status_code == 404:
            return SentinelClientError("caller_entity_not_found", message)
        return SentinelClientError(
            "sentinel_entity_request_failed",
            message,
            retryable=response.status_code == 429 or response.status_code >= 500,
        )

    @classmethod
    def _application_response_error(cls, response: httpx.Response) -> SentinelClientError:
        message = cls._error_message(response)
        if response.status_code == 401:
            return SentinelClientError("sentinel_authentication_failed", message)
        if response.status_code == 403:
            return SentinelClientError("sentinel_application_read_denied", message)
        if response.status_code == 404:
            return SentinelClientError("sentinel_application_not_found", message)
        return SentinelClientError(
            "sentinel_application_request_failed",
            message,
            retryable=response.status_code == 429 or response.status_code >= 500,
        )

    @classmethod
    def _api_response_error(cls, response: httpx.Response) -> SentinelClientError:
        message = cls._error_message(response)
        if response.status_code == 400:
            return SentinelClientError("sentinel_api_invalid_request", message)
        if response.status_code == 401:
            return SentinelClientError("sentinel_api_authentication_failed", message)
        if response.status_code == 403:
            return SentinelClientError("sentinel_api_access_denied", message)
        if response.status_code == 404:
            return SentinelClientError("sentinel_api_resource_not_found", message)
        if response.status_code == 409:
            return SentinelClientError("sentinel_api_conflict", message)
        if response.status_code == 429:
            return SentinelClientError("sentinel_api_rate_limited", message, retryable=True)
        if response.status_code >= 500:
            return SentinelClientError("sentinel_api_unavailable", message, retryable=True)
        return SentinelClientError("sentinel_api_request_failed", message)

    @staticmethod
    def _error_message(response: httpx.Response) -> str:
        fallback = f"Sentinel returned HTTP {response.status_code}."
        try:
            body = response.json()
        except ValueError:
            return fallback
        if not isinstance(body, dict):
            return fallback
        error = body.get("error")
        return error if isinstance(error, str) and error.strip() else fallback
