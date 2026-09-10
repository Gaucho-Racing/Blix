from enum import StrEnum
from typing import Self

from pydantic import model_validator

from blix_agent_worker.models.common import ContractModel, EntityID, ServiceAccountID, UserID


class EntityType(StrEnum):
    USER = "USER"
    SERVICE_ACCOUNT = "SERVICE_ACCOUNT"


class CallerContext(ContractModel):
    entity_id: EntityID
    entity_type: EntityType
    user_id: UserID | None = None
    service_account_id: ServiceAccountID | None = None

    @model_validator(mode="after")
    def validate_identity(self) -> Self:
        if self.entity_type is EntityType.USER:
            if self.user_id is None or self.service_account_id is not None:
                raise ValueError("a user caller must contain only user_id")
        elif self.service_account_id is None or self.user_id is not None:
            raise ValueError("a service-account caller must contain only service_account_id")
        return self
