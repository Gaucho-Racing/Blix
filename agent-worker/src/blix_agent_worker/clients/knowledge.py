import os
from typing import Self

import httpx

from blix_agent_worker.models.common import JsonObject


class KnowledgeClientError(Exception):
    def __init__(self, code: str, message: str, *, retryable: bool = False) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class KnowledgeClient:
    def __init__(self, *, transport: httpx.AsyncBaseTransport | None = None) -> None:
        self._client = httpx.AsyncClient(
            transport=transport,
            timeout=60,
            follow_redirects=False,
            headers={"Authorization": f"Bearer {os.environ.get('BLIX_INTERNAL_TOKEN', '')}"},
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self._client.aclose()

    async def request(
        self,
        base_url: str,
        method: str,
        path: str,
        *,
        params: dict[str, str | int] | None = None,
        payload: JsonObject | None = None,
        timeout: float = 60,
    ) -> JsonObject:
        try:
            response = await self._client.request(
                method,
                f"{base_url.rstrip('/')}/internal/v1/{path}",
                params=params,
                json=payload,
                timeout=timeout,
            )
        except httpx.RequestError as error:
            raise KnowledgeClientError(
                "knowledge_unavailable",
                "The knowledge service is unavailable.",
                retryable=True,
            ) from error
        if not response.is_success:
            code = {
                400: "invalid_knowledge_request",
                403: "knowledge_forbidden",
                404: "knowledge_not_found",
                409: "knowledge_conflict",
            }.get(response.status_code, "knowledge_request_failed")
            message = f"The knowledge service returned HTTP {response.status_code}."
            if response.status_code == 409:
                if path == "source-records" or path.startswith("sources"):
                    message += (
                        " The source may be disabled or an existing record version has different content. "
                        "Read the source/record first; do not overwrite evidence or invent a version "
                        "to bypass a conflict."
                    )
                elif path.endswith("/apply"):
                    message += (
                        " Publication may already be running or its base revision may have changed. "
                        "Inspect this changeset and retry the same ID when appropriate. Do not replace "
                        "a changeset whose publication outcome is unresolved."
                    )
                else:
                    message += " Read the latest memory revision; do not force the write."
            raise KnowledgeClientError(
                code, message, retryable=response.status_code == 429 or response.status_code >= 500
            )
        try:
            result = response.json()
        except ValueError as error:
            raise KnowledgeClientError(
                "invalid_knowledge_response", "The knowledge service returned invalid JSON."
            ) from error
        if not isinstance(result, dict):
            raise KnowledgeClientError("invalid_knowledge_response", "The knowledge response must be an object.")
        return result
