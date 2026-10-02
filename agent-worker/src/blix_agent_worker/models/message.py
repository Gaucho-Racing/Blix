from datetime import datetime
from enum import StrEnum
from typing import Self

from pydantic import ConfigDict, model_validator

from blix_agent_worker.models.common import ContractModel, ConversationID, MessageID, NonEmptyText, SessionID


class ConversationMessageRole(StrEnum):
    USER = "user"
    ASSISTANT = "assistant"


class ConversationMessage(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False, extra="ignore")

    message_id: MessageID
    conversation_id: ConversationID
    session_id: SessionID
    event_position: int = 0
    turn_id: str = ""
    role: ConversationMessageRole
    content: NonEmptyText
    created_at: datetime

    @model_validator(mode="after")
    def created_at_must_include_timezone(self) -> Self:
        if self.created_at.tzinfo is None or self.created_at.utcoffset() is None:
            raise ValueError("created_at must include a timezone")
        return self
