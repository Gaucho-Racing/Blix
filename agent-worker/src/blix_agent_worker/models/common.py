from typing import Annotated, cast

import ulid
from pydantic import BaseModel, BeforeValidator, ConfigDict, Field, JsonValue, ValidationError


def _normalize_ulid(value: object) -> object:
    if isinstance(value, ulid.ULID):
        return str(value)
    if not isinstance(value, str):
        return value
    try:
        return str(ulid.parse_strict(value))
    except ulid.ULIDError as error:
        raise ValueError("invalid ULID") from error


Identifier = Annotated[str, Field(min_length=1, max_length=64, pattern=r"^[a-z][a-z0-9_]*$")]
VersionedIdentifier = Annotated[
    str,
    Field(min_length=4, max_length=64, pattern=r"^[a-z][a-z0-9_]*_v[1-9][0-9]*$"),
]
NonEmptyText = Annotated[str, Field(min_length=1)]
PositiveSeconds = Annotated[int, Field(gt=0)]
ULID = Annotated[
    str,
    Field(min_length=26, max_length=26, pattern=r"^[0-7][0123456789abcdefghjkmnpqrstvwxyz]{25}$"),
    BeforeValidator(_normalize_ulid),
]
type JsonObject = dict[str, JsonValue]


def new_ulid() -> ULID:
    return cast(ULID, str(ulid.make()))


def validation_error_details(error: ValidationError) -> JsonObject:
    errors: list[JsonValue] = []
    for item in error.errors(include_url=False, include_context=False, include_input=False):
        detail: JsonObject = {
            "location": ".".join(str(part) for part in item["loc"]),
            "message": item["msg"],
            "type": item["type"],
        }
        errors.append(detail)
    details: JsonObject = {"errors": errors}
    return details


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
