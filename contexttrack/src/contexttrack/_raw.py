"""Private strict models for the current unversioned Go capture format."""

from typing import Literal, Self

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator


class _RawModel(BaseModel):
    model_config = ConfigDict(strict=True, extra="forbid")


class RawContext(_RawModel):
    """Context evidence, excluding the historical root-address format."""

    context_id: str | None = None
    source: str | None = None
    type: str | None = None
    error: str | None = None


class _PresentStrings(_RawModel):
    """Missing string fields default to None; explicitly supplied nulls are invalid."""

    @field_validator("*", mode="before")
    @classmethod
    def reject_explicit_null(cls, value: object) -> object:
        # Defaults remain unvalidated, distinguishing omission from explicit null.
        if value is None:
            raise ValueError("present raw message/request-label fields must be strings")
        return value


class RawRequestID(_PresentStrings):
    """Outbound endpoint labels, not an occurrence or correlation identifier."""

    method: str | None = None
    host: str | None = None
    path: str | None = None


class RawMessage(_PresentStrings):
    """Known dotted wire keys; RawEvent restricts the subset allowed for its kind."""

    method: str | None = Field(default=None, validation_alias="req.Method")
    host: str | None = Field(default=None, validation_alias="req.URL.Host")
    path: str | None = Field(default=None, validation_alias="req.URL.Path")
    raw_query: str | None = Field(default=None, validation_alias="req.URL.RawQuery")
    pattern: str | None = None
    code: str | None = None
    status_code: str | None = Field(default=None, validation_alias="resp.StatusCode")


# These are internal field names, as reported by RawMessage.model_fields_set.
_ALLOWED_MESSAGE_FIELDS = {
    "Request sent": {"method", "host", "path", "raw_query"},
    "Request received": {"method", "path", "raw_query"},
    "Request routed": {"method", "path", "pattern"},
    "Response sent": {"method", "path", "code"},
    "Response received": {"method", "path", "status_code"},
}


class RawEvent(_RawModel):
    """One raw record; optional evidence never requires another record to validate."""

    kind: Literal[
        "Request sent",
        "Request received",
        "Request routed",
        "Response sent",
        "Response received",
    ]
    pid: int
    message: RawMessage
    context: RawContext | None = None
    request_id: RawRequestID | None = None
    api_id: str | None = None
    handler: str | None = None
    goroutine_id: int | None = None
    thread_id: int | None = None
    file: str | None = None
    line: int | None = None

    @model_validator(mode="after")
    def validate_kind_fields(self) -> Self:
        unexpected = self.message.model_fields_set - _ALLOWED_MESSAGE_FIELDS[self.kind]
        if unexpected:
            raise ValueError(
                f"message fields not allowed for {self.kind}: {', '.join(sorted(unexpected))}"
            )
        if self.request_id is not None and self.kind != "Request sent":
            raise ValueError("request_id is only allowed on Request sent")
        return self
