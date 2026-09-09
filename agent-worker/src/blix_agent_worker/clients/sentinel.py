from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from typing import Literal, Self

import httpx
from pydantic import BaseModel, ConfigDict, Field, SecretStr

from blix_agent_worker.models.common import ExecutionID, RunID


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


@dataclass(frozen=True, slots=True)
class SentinelCredential:
    token_id: str
    user_id: str
    application_id: str
    run_id: RunID
    execution_id: ExecutionID
    token_type: Literal["Bearer"]
    scope: str
    expires_at: datetime
    access_token: str = field(repr=False)


class SentinelClient:
    def __init__(self) -> None:
        self._client = httpx.AsyncClient(
            headers={"Accept": "application/json", "Content-Type": "application/json"},
            timeout=30,
        )
        self._credentials: dict[str, SentinelCredential] = {}

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        self._credentials.clear()
        await self._client.aclose()

    async def impersonate(
        self,
        *,
        sentinel_url: str,
        service_account_token: str,
        user_id: str,
        application_id: str,
        run_id: RunID,
        execution_id: ExecutionID,
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
            execution_id=execution_id,
            token_type=payload.token_type,
            scope=payload.scope,
            expires_at=requested_at + timedelta(seconds=payload.expires_in),
        )
        self._remove_expired_credentials()
        self._credentials[credential.token_id] = credential
        return credential

    def get_credential(self, token_id: str, *, run_id: RunID, execution_id: ExecutionID) -> SentinelCredential:
        credential = self._credentials.get(token_id)
        if credential is None:
            raise SentinelCredentialNotFoundError(token_id)
        if credential.expires_at <= datetime.now(UTC):
            self._credentials.pop(token_id, None)
            raise SentinelCredentialNotFoundError(token_id)
        if credential.run_id != run_id or credential.execution_id != execution_id:
            raise SentinelCredentialNotFoundError(token_id)
        return credential

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
