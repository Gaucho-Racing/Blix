from typing import Protocol

from blix_agent_worker.models.llm import ModelRequest, ModelResponse


class ModelClientError(Exception):
    def __init__(
        self,
        code: str,
        message: str,
        retryable: bool = False,
        *,
        http_status: int | None = None,
        retry_after_seconds: float | None = None,
    ) -> None:
        self.code = code
        self.retryable = retryable
        self.http_status = http_status
        self.retry_after_seconds = retry_after_seconds
        super().__init__(message)


class LanguageModel(Protocol):
    async def complete(self, request: ModelRequest) -> ModelResponse: ...
