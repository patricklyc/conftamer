"""Strict, immutable wire models for normalized ContextTrack v1 events."""

from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, TypeAdapter, field_validator

__all__ = [
    "EVENT_ADAPTER",
    "ContextInfo",
    "Event",
    "RequestFields",
    "RequestMessage",
    "RequestReceived",
    "RequestRouted",
    "RequestSent",
    "ResponseMessage",
    "ResponseReceived",
    "ResponseSent",
    "RoutedRequestMessage",
    "SentRequestMessage",
]


class _StrictModel(BaseModel):
    model_config = ConfigDict(
        strict=True, extra="forbid", frozen=True, revalidate_instances="always"
    )


class ContextInfo(_StrictModel):
    """Process-local context evidence and diagnostics, without inferred identity."""

    context_id: str | None = None
    source: str | None = None
    type: str | None = None
    error: str | None = None


class RequestFields(_StrictModel):
    """Request labels shared by all payloads; present paths must be nonempty."""

    method: str | None = None
    path: Annotated[str, Field(min_length=1)] | None = None


class RequestMessage(RequestFields):
    """Received request evidence, including the query string when available."""

    raw_query: str | None = None


class SentRequestMessage(RequestMessage):
    """Outbound endpoint labels and query evidence, not an occurrence ID."""

    host: str | None = None


class RoutedRequestMessage(RequestFields):
    """A routing observation, retaining both concrete path and handler pattern."""

    pattern: str | None = None


class ResponseMessage(RequestFields):
    """Response evidence without enrichment from other events."""

    status_code: Annotated[int, Field(ge=0)] | None = None


class _Envelope(_StrictModel):
    schema_version: Literal[1]
    kind: str
    pid: int
    context: ContextInfo
    api_id: str | None = None
    handler: str | None = None
    goroutine_id: int | None = None
    thread_id: int | None = None
    file: str | None = None
    line: int | None = None

    @field_validator("schema_version", mode="before")
    @classmethod
    def exact_version_type(cls, value: object) -> object:
        # Strict Literal[1] alone also accepts True and 1.0 in Pydantic 2.13.5.
        if type(value) is not int:
            raise ValueError("schema_version must be an integer")
        return value

    @property
    def context_key(self) -> tuple[int, str] | None:
        """Return a capture-local influence group, never a distributed trace ID."""
        context_id = self.context.context_id
        return (self.pid, context_id) if context_id else None


class RequestSent(_Envelope):
    """An outbound request observation."""

    kind: Literal["send_request"]
    message: SentRequestMessage


class RequestReceived(_Envelope):
    """An inbound request observation."""

    kind: Literal["receive_request"]
    message: RequestMessage


class RequestRouted(_Envelope):
    """A handler-routing observation, distinct from a message send or receive."""

    kind: Literal["request_routed"]
    message: RoutedRequestMessage


class ResponseSent(_Envelope):
    """An outbound response observation."""

    kind: Literal["send_response"]
    message: ResponseMessage


class ResponseReceived(_Envelope):
    """An inbound response observation, including repeated hooks."""

    kind: Literal["receive_response"]
    message: ResponseMessage


Event = Annotated[
    RequestSent | RequestReceived | RequestRouted | ResponseSent | ResponseReceived,
    Field(discriminator="kind"),
]
EVENT_ADAPTER: TypeAdapter[Event] = TypeAdapter(Event)
