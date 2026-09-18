from typing import Annotated, Literal, Self

from pydantic import AwareDatetime, BaseModel, ConfigDict, Field, HttpUrl, TypeAdapter, field_validator, model_validator

from blix_agent_worker.models.common import ContractModel, JsonObject, SourceID, SourceRecordID

SourceKind = Annotated[str, Field(min_length=1, max_length=64, pattern=r"^[a-z][a-z0-9_]*$")]
ExternalID = Annotated[str, Field(min_length=1, max_length=512)]


class SourceDefinition(ContractModel):
    kind: SourceKind
    external_id: ExternalID
    name: Annotated[str, Field(min_length=1, max_length=512)]
    metadata: JsonObject = Field(default_factory=dict)


class SourceAuthor(ContractModel):
    provider: SourceKind
    external_id: ExternalID
    display_name: Annotated[str, Field(max_length=512)] = ""
    username: Annotated[str, Field(max_length=512)] = ""


class SourceRecordCapture(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)

    source_id: SourceID | None = None
    source: SourceDefinition | None = None
    external_id: ExternalID
    external_version: Annotated[str, Field(max_length=512)] = ""
    record_type: SourceKind
    parent_external_id: Annotated[str, Field(max_length=512)] = ""
    external_url: Annotated[str, Field(max_length=4096)] = ""
    content: Annotated[str, Field(min_length=1, max_length=1024 * 1024)]
    evidence_type: Literal["quoted_source", "agent_summary"]
    occurred_at: AwareDatetime
    author: SourceAuthor | None = None
    metadata: JsonObject = Field(default_factory=dict)

    @model_validator(mode="after")
    def validate_capture(self) -> Self:
        if (self.source_id is None) == (self.source is None):
            raise ValueError("provide exactly one of source_id or source")
        if not self.external_id.strip():
            raise ValueError("external_id cannot be blank")
        if not self.content.strip() or "\x00" in self.content or len(self.content.encode("utf-8")) > 1024 * 1024:
            raise ValueError("content must be nonempty UTF-8 text up to 1 MiB without NUL characters")
        return self

    @field_validator("external_url")
    @classmethod
    def validate_url(cls, value: str) -> str:
        if value:
            url = TypeAdapter(HttpUrl).validate_python(value)
            if url.username or url.password:
                raise ValueError("source URLs cannot contain credentials")
        return value


class StoredSource(BaseModel):
    model_config = ConfigDict(extra="ignore")
    source_id: SourceID
    kind: str
    external_id: str
    name: str
    status: Literal["active", "disabled"]


class StoredSourceRecord(BaseModel):
    model_config = ConfigDict(extra="ignore")
    source_record_id: SourceRecordID
    source_id: SourceID
    external_id: str
    external_version: str
    evidence_type: Literal["quoted_source", "agent_summary"]
    capture_method: Literal["agent_relay"]


class CapturedSourceRecord(BaseModel):
    model_config = ConfigDict(extra="ignore")
    record: StoredSourceRecord
    created: bool
