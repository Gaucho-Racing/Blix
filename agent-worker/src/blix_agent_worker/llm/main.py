from typing import Protocol

from blix_agent_worker.models.llm import ModelRequest, ModelResponse


class ModelClientError(Exception):
    def __init__(self, code: str, message: str, retryable: bool = False) -> None:
        self.code = code
        self.retryable = retryable
        super().__init__(message)


class LanguageModel(Protocol):
    async def complete(self, request: ModelRequest) -> ModelResponse: ...
