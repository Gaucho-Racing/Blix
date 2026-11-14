import json
from dataclasses import dataclass
from typing import Self
from urllib.parse import quote

import httpx
from pydantic import BaseModel, ConfigDict, Field


class DepotClientError(Exception):
    def __init__(self, code: str, message: str, *, retryable: bool = False) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class _DepotFile(BaseModel):
    model_config = ConfigDict(extra="ignore")

    id: str = Field(min_length=1)
    public: bool
    size_bytes: int


@dataclass(frozen=True, slots=True)
class DepotPublicFile:
    id: str
    url: str
    size_bytes: int


class DepotClient:
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

    async def upload_public(
        self,
        *,
        base_url: str,
        bucket: str,
        token: str,
        filename: str,
        content_type: str,
        data: bytes,
        tags: dict[str, str],
    ) -> DepotPublicFile:
        base_url = base_url.rstrip("/")
        try:
            response = await self._client.post(
                f"{base_url}/buckets/{quote(bucket, safe='')}/files",
                headers={"Authorization": f"Bearer {token}"},
                data={
                    "original_name": filename,
                    "content_type": content_type,
                    "public": "true",
                    "tags": json.dumps(tags, separators=(",", ":")),
                },
                files={"file": (filename, data, content_type)},
            )
        except httpx.RequestError as error:
            raise DepotClientError("depot_unavailable", "Depot is unavailable.", retryable=True) from error

        if response.is_error:
            raise self._response_error(response)
        try:
            file = _DepotFile.model_validate(response.json())
        except ValueError as error:
            raise DepotClientError("invalid_depot_response", "Depot returned an invalid file.") from error
        if not file.public:
            raise DepotClientError("depot_file_not_public", "Depot stored the file without public access.")
        return DepotPublicFile(
            id=file.id,
            url=f"{base_url}/download/{quote(file.id, safe='')}",
            size_bytes=file.size_bytes,
        )

    @staticmethod
    def _response_error(response: httpx.Response) -> DepotClientError:
        status = response.status_code
        message = f"Depot returned HTTP {status}."
        try:
            body = response.json()
        except ValueError:
            body = None
        if isinstance(body, dict) and isinstance(body.get("error"), str) and body["error"].strip():
            message = body["error"]
        if status == 401:
            return DepotClientError("depot_authentication_failed", message)
        if status == 403:
            return DepotClientError("depot_upload_denied", message)
        if status == 404:
            return DepotClientError("depot_bucket_not_found", message)
        return DepotClientError("depot_upload_failed", message, retryable=status == 429 or status >= 500)
