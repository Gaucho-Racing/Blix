from typing import Self

import httpx

from blix_agent_worker.models.common import JsonObject


class TavilyClientError(Exception):
    def __init__(self, code: str, message: str, *, retryable: bool = False) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class TavilyClient:
    def __init__(self, *, api_key: str, timeout_seconds: float = 30) -> None:
        self._api_key = api_key.strip()
        self._client = httpx.AsyncClient(
            base_url="https://api.tavily.com/",
            headers={"Accept": "application/json", "Content-Type": "application/json"},
            timeout=httpx.Timeout(timeout_seconds),
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._client.aclose()

    async def search(self, payload: JsonObject) -> JsonObject:
        return await self._post("search", payload)

    async def extract(self, payload: JsonObject) -> JsonObject:
        return await self._post("extract", payload)

    async def _post(self, path: str, payload: JsonObject) -> JsonObject:
        if not self._api_key:
            raise TavilyClientError(
                "web_research_not_configured",
                "Web research requires TAVILY_API_KEY.",
            )

        try:
            response = await self._client.post(
                path,
                headers={"Authorization": f"Bearer {self._api_key}"},
                json=payload,
            )
        except httpx.TimeoutException as error:
            raise TavilyClientError("web_provider_timeout", "The web provider timed out.", retryable=True) from error
        except httpx.RequestError as error:
            raise TavilyClientError(
                "web_provider_unavailable",
                "The web provider is unavailable.",
                retryable=True,
            ) from error

        if response.is_error:
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
