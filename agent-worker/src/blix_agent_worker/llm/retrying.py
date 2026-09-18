import asyncio
import logging
import random
import time
from collections.abc import Awaitable, Callable

from blix_agent_worker.llm.main import LanguageModel, ModelClientError
from blix_agent_worker.models.common import ContractError, new_ulid
from blix_agent_worker.models.llm import ModelAttemptEvent, ModelRequest, ModelResponse

logger = logging.getLogger(__name__)


class RetryingModel:
    MAX_ATTEMPTS = 3
    MAX_RETRY_DELAY_SECONDS = 60

    def __init__(
        self,
        model: LanguageModel,
        model_name: str,
        on_attempt: Callable[[ModelAttemptEvent], Awaitable[None]],
        on_text_delta: Callable[[str, int, int, str], Awaitable[None]] | None = None,
    ) -> None:
        self._model = model
        self._model_name = model_name
        self._on_attempt = on_attempt
        self._on_text_delta = on_text_delta

    async def complete(self, request: ModelRequest) -> ModelResponse:
        for attempt in range(1, self.MAX_ATTEMPTS + 1):
            call = ModelAttemptEvent(
                type="model.call_v1",
                call_id=new_ulid("modelcall"),
                model=self._model_name,
                sequence=request.sequence,
                attempt=attempt,
                max_attempts=self.MAX_ATTEMPTS,
                request={
                    "message_count": len(request.messages),
                    "callable_ids": [definition.id for definition in request.callables],
                    "output_format": request.output_format.id if request.output_format else None,
                    "has_continuation": request.continuation is not None,
                },
            )
            await self._on_attempt(call)
            started = time.monotonic()
            try:
                async def relay(delta: str) -> None:
                    if self._on_text_delta is not None:
                        await self._on_text_delta(call.call_id, request.sequence, attempt, delta)

                response = await self._model.complete(request, on_text_delta=relay)
            except ModelClientError as error:
                delay = None
                if error.retryable and attempt < self.MAX_ATTEMPTS:
                    delay = max(2**attempt + random.uniform(0, 1), error.retry_after_seconds or 0)
                    if delay > self.MAX_RETRY_DELAY_SECONDS:
                        delay = None
                await self._on_attempt(
                    call.model_copy(
                        update={
                            "type": "model.failure_v1",
                            "request": {},
                            "elapsed_ms": round((time.monotonic() - started) * 1000),
                            "error": ContractError(code=error.code, message=str(error), retryable=error.retryable),
                            "http_status": error.http_status,
                            "retry_in_seconds": delay,
                        }
                    )
                )
                if delay is None:
                    raise
                logger.warning(
                    "model call %s sequence=%d attempt=%d/%d failed code=%s status=%s; retrying in %.2fs",
                    call.call_id, request.sequence, attempt, self.MAX_ATTEMPTS, error.code, error.http_status, delay,
                )
                await asyncio.sleep(delay)
                continue
            return response.model_copy(
                update={
                    "call_id": call.call_id,
                    "attempt": attempt,
                    "elapsed_ms": round((time.monotonic() - started) * 1000),
                }
            )
        raise RuntimeError("model attempts exhausted without a result")
