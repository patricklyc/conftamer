from typing import Annotated, Literal, Self

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    TypeAdapter,
    field_validator,
    model_validator,
)

Counter = Annotated[int, Field(strict=True, ge=1, le=2**64 - 1)]
Text = Annotated[str, Field(min_length=1)]
ProcessID = Annotated[str, Field(pattern=r"^[0-9a-f]{32}$")]


class Model(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)


class RequestLabel(Model):
    method: Text
    host: Text | None
    path: str


class Envelope(Model):
    schema_version: Literal[3]
    capture_id: Text
    process_id: ProcessID
    seq: Counter
    exchange_id: Counter

    @field_validator("schema_version", mode="before")
    @classmethod
    def integer_version(cls, value: object) -> object:
        if type(value) is not int:
            raise ValueError("schema_version must be an integer")
        return value


class RequestEvent(Envelope):
    kind: Literal["send_request", "receive_request"]
    context_id: Counter | None
    request: RequestLabel

    @model_validator(mode="after")
    def client_host(self) -> Self:
        if self.kind == "send_request" and self.request.host is None:
            raise ValueError("send_request requires a host")
        return self


class ResponseEvent(Envelope):
    kind: Literal["send_response", "receive_response"]
    status_code: Annotated[int, Field(strict=True, ge=100, le=999)]

    @model_validator(mode="after")
    def terminal_status(self) -> Self:
        if self.status_code < 200 and self.status_code != 101:
            raise ValueError("only terminal responses are recorded")
        return self


class MessageLabel(Model):
    kind: Literal[
        "send_request",
        "receive_request",
        "send_response",
        "receive_response",
    ]
    method: Text
    host: Text | None
    path: Text
    status_code: Annotated[int, Field(strict=True, ge=100, le=999)] | None

    @model_validator(mode="after")
    def endpoint_shape(self) -> Self:
        is_client = self.kind in ("send_request", "receive_response")
        if is_client and self.host is None:
            raise ValueError("client messages require a host")
        if not is_client and self.host is not None:
            raise ValueError("server messages cannot have a host")
        return self

    @model_validator(mode="after")
    def status_shape(self) -> Self:
        is_response = self.kind in ("send_response", "receive_response")
        if is_response and self.status_code is None:
            raise ValueError("response messages require a status")
        if not is_response and self.status_code is not None:
            raise ValueError("request messages cannot have a status")
        if (
            self.status_code is not None
            and self.status_code < 200
            and self.status_code != 101
        ):
            raise ValueError("only terminal responses are recorded")
        return self


Event = Annotated[RequestEvent | ResponseEvent, Field(discriminator="kind")]
EVENT_ADAPTER = TypeAdapter(Event)
