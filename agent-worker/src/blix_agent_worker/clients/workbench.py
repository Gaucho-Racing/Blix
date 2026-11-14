from typing import Self

import httpx
from pydantic import JsonValue


class WorkbenchClientError(Exception):
    def __init__(self, code: str, message: str, *, status_code: int | None = None, retryable: bool = False) -> None:
        self.code = code
        self.status_code = status_code
        self.retryable = retryable
        super().__init__(message)


class WorkbenchClient:
    def __init__(self, *, transport: httpx.AsyncBaseTransport | None = None) -> None:
        self._client = httpx.AsyncClient(
            headers={"Accept": "application/json"},
            timeout=httpx.Timeout(60, connect=10),
            transport=transport,
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._client.aclose()

    async def get(
        self,
        *,
        base_url: str,
        access_token: str,
        path: str,
        query: dict[str, str] | None = None,
    ) -> JsonValue:
        return await self._request("GET", base_url=base_url, access_token=access_token, path=path, params=query)

    async def post(self, *, base_url: str, access_token: str, path: str, body: dict[str, JsonValue]) -> JsonValue:
        return await self._request("POST", base_url=base_url, access_token=access_token, path=path, json=body)

    async def _request(
        self,
        method: str,
        *,
        base_url: str,
        access_token: str,
        path: str,
        params: dict[str, str] | None = None,
        json: dict[str, JsonValue] | None = None,
    ) -> JsonValue:
        try:
            response = await self._client.request(
                method,
                f"{base_url.rstrip('/')}{path}",
                headers={"Authorization": f"Bearer {access_token}"},
                params=params,
                json=json,
            )
        except httpx.TimeoutException as error:
            raise WorkbenchClientError(
                "workbench_timeout", "Workbench did not respond in time.", retryable=True
            ) from error
        except httpx.RequestError as error:
            raise WorkbenchClientError("workbench_unavailable", "Workbench is unavailable.", retryable=True) from error

        if response.is_error:
            raise self._response_error(response)
        try:
            return response.json()
        except ValueError as error:
            raise WorkbenchClientError("invalid_workbench_response", "Workbench returned non-JSON data.") from error

    @staticmethod
    def _response_error(response: httpx.Response) -> WorkbenchClientError:
        status = response.status_code
        message = f"Workbench returned HTTP {status}."
        upstream_code = ""
        try:
            body = response.json()
        except ValueError:
            body = None
        if isinstance(body, dict):
            if isinstance(body.get("error"), str) and body["error"].strip():
                message = body["error"]
            if isinstance(body.get("code"), str):
                upstream_code = body["code"]

        if upstream_code == "read_only_violation":
            return WorkbenchClientError("workbench_read_only_violation", message, status_code=status)
        if status == 400:
            return WorkbenchClientError("workbench_query_failed", message, status_code=status)
        if status == 401:
            return WorkbenchClientError("workbench_authentication_failed", message, status_code=status)
        if status == 403:
            return WorkbenchClientError("workbench_access_denied", message, status_code=status)
        if status == 404:
            return WorkbenchClientError("workbench_resource_not_found", message, status_code=status)
        if status == 429:
            return WorkbenchClientError("workbench_rate_limited", message, status_code=status, retryable=True)
        if status >= 500:
            return WorkbenchClientError("workbench_unavailable", message, status_code=status, retryable=True)
        return WorkbenchClientError("workbench_request_failed", message, status_code=status)
