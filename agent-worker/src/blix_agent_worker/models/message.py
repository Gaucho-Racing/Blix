from datetime import datetime
from enum import StrEnum
from typing import Self

from pydantic import model_validator

from blix_agent_worker.models.common import ULID, ContractModel, NonEmptyText


class ConversationMessageRole(StrEnum):
    USER = "user"
    ASSISTANT = "assistant"


class ConversationMessage(ContractModel):
    message_id: ULID
    run_id: ULID
    parent_execution_id: ULID
    role: ConversationMessageRole
    content: NonEmptyText
    created_at: datetime

    @model_validator(mode="after")
    def created_at_must_include_timezone(self) -> Self:
        if self.created_at.tzinfo is None or self.created_at.utcoffset() is None:
            raise ValueError("created_at must include a timezone")
        return self
