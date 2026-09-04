from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field, JsonValue

Identifier = Annotated[str, Field(min_length=1, max_length=64, pattern=r"^[a-z][a-z0-9_]*$")]
VersionedIdentifier = Annotated[
    str,
    Field(min_length=4, max_length=64, pattern=r"^[a-z][a-z0-9_]*_v[1-9][0-9]*$"),
]
NonEmptyText = Annotated[str, Field(min_length=1)]
PositiveSeconds = Annotated[int, Field(gt=0)]
type JsonObject = dict[str, JsonValue]


class ContractModel(BaseModel):
    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        str_strip_whitespace=True,
        validate_default=True,
    )


class ContractError(ContractModel):
    code: Identifier
    message: NonEmptyText
    retryable: bool = False
    details: JsonObject = Field(default_factory=dict)
