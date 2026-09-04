from typing import Self

import httpx

from blix_agent_worker.models.common import JsonObject


class TavilyClientError(Exception):
    def __init__(self, code: str, message: str, *, retryable: bool = False) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class TavilyClient:
    def __init__(self) -> None:
        self._client = httpx.AsyncClient(
            base_url="https://api.tavily.com/",
            headers={"Accept": "application/json", "Content-Type": "application/json"},
            timeout=None,
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._client.aclose()

    async def search(self, payload: JsonObject, *, api_key: str) -> JsonObject:
        return await self._post("search", payload, api_key=api_key)

    async def extract(self, payload: JsonObject, *, api_key: str) -> JsonObject:
        return await self._post("extract", payload, api_key=api_key)

    async def _post(self, path: str, payload: JsonObject, *, api_key: str) -> JsonObject:
        if not api_key.strip():
            raise ValueError("api_key cannot be empty")

        try:
            response = await self._client.post(
                path,
                headers={"Authorization": f"Bearer {api_key}"},
                json=payload,
            )
        except httpx.RequestError as error:
            raise TavilyClientError(
                "web_provider_unavailable",
                "The web provider is unavailable.",
                retryable=True,
            ) from error

        if response.is_error:
            if response.status_code == 401:
                raise TavilyClientError(
                    "web_provider_authentication_failed",
                    "The web provider rejected its configured credentials.",
                )
            retryable = response.status_code == 429 or response.status_code >= 500
            raise TavilyClientError(
                "web_provider_request_failed",
                self._error_message(response),
                retryable=retryable,
            )

        try:
            body = response.json()
        except ValueError as error:
            raise TavilyClientError(
                "invalid_web_provider_response",
                "The web provider returned invalid JSON.",
            ) from error
        if not isinstance(body, dict):
            raise TavilyClientError(
                "invalid_web_provider_response",
                "The web provider returned an invalid response.",
            )
        return body

    @staticmethod
    def _error_message(response: httpx.Response) -> str:
        fallback = f"The web provider returned HTTP {response.status_code}."
        try:
            body = response.json()
        except ValueError:
            return fallback
        if not isinstance(body, dict):
            return fallback
        detail = body.get("detail")
        if isinstance(detail, dict) and isinstance(detail.get("error"), str):
            return detail["error"]
        if isinstance(detail, str):
            return detail
        return fallback
