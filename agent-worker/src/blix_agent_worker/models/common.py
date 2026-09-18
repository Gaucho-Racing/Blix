from collections.abc import Callable
from typing import Annotated, Literal

import ulid
from pydantic import BaseModel, BeforeValidator, ConfigDict, Field, JsonValue, ValidationError

IdentifierPrefix = Literal[
    "run",
    "execution",
    "event",
    "message",
    "call",
    "modelcall",
    "worker",
    "outbox",
    "job",
    "notification",
    "ent",
    "usr",
    "sa",
    "source",
    "sourcerecord",
    "changeset",
]


def _prefixed_ulid_validator(prefix: IdentifierPrefix) -> Callable[[object], object]:
    def validate(value: object) -> object:
        if not isinstance(value, str):
            return value
        try:
            parsed_prefix, parsed = ulid.parse_prefixed(value)
        except ulid.ULIDError as error:
            raise ValueError("invalid prefixed ULID") from error
        if parsed_prefix != prefix or parsed.prefixed(parsed_prefix) != value:
            raise ValueError(f"identifier must be a canonical {prefix}_ prefixed ULID")
        return value

    return validate


def _prefixed_ulid(prefix: IdentifierPrefix) -> object:
    length = len(prefix) + 27
    return Annotated[
        str,
        Field(min_length=length, max_length=length),
        BeforeValidator(_prefixed_ulid_validator(prefix)),
    ]


RunID = _prefixed_ulid("run")
ExecutionID = _prefixed_ulid("execution")
EventID = _prefixed_ulid("event")
MessageID = _prefixed_ulid("message")
CallID = _prefixed_ulid("call")
ModelCallID = _prefixed_ulid("modelcall")
WorkerID = _prefixed_ulid("worker")
OutboxID = _prefixed_ulid("outbox")
JobID = _prefixed_ulid("job")
NotificationID = _prefixed_ulid("notification")
EntityID = _prefixed_ulid("ent")
UserID = _prefixed_ulid("usr")
ServiceAccountID = _prefixed_ulid("sa")
SourceID = _prefixed_ulid("source")
SourceRecordID = _prefixed_ulid("sourcerecord")
ChangesetID = _prefixed_ulid("changeset")


def new_ulid(prefix: IdentifierPrefix) -> str:
    return ulid.make().prefixed(prefix)


Identifier = Annotated[str, Field(min_length=1, max_length=64, pattern=r"^[a-z][a-z0-9_]*$")]
VersionedIdentifier = Annotated[
    str,
    Field(min_length=4, max_length=64, pattern=r"^[a-z][a-z0-9_]*_v[1-9][0-9]*$"),
]
NonEmptyText = Annotated[str, Field(min_length=1)]
type JsonObject = dict[str, JsonValue]


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
