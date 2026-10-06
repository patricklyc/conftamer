"""Pure, record-local conversion from raw capture data to normalized v1 events."""

from contexttrack._raw import RawContext, RawEvent, RawRequestID
from contexttrack.models import (
    EVENT_ADAPTER,
    Event,
    RequestFields,
    RequestMessage,
    ResponseMessage,
    RoutedRequestMessage,
    SentRequestMessage,
)

__all__ = ["normalize_record"]


def _normalize_path(value: str | None) -> str | None:
    return "/" if value == "" else value


def _parse_status_code(value: str | None) -> int | None:
    if value is None:
        return None
    if not value.isdecimal():
        raise ValueError("status code must be a decimal string")
    return int(value)


def normalize_record(record: object) -> Event:
    """Validate and normalize one raw record without mutation, I/O, or association.

    Invalid raw shapes raise Pydantic ValidationError (a ValueError); invalid
    decimal status strings raise ValueError. Incomplete evidence stays null.
    """
    raw = RawEvent.model_validate(record)
    message = raw.message
    normalized_message: RequestFields
    match raw.kind:
        case "Request sent":
            kind = "send_request"
            # Endpoint labels come only from request_id, never from message.
            endpoint = raw.request_id if raw.request_id is not None else RawRequestID()
            normalized_message = SentRequestMessage(
                method=endpoint.method,
                path=_normalize_path(endpoint.path),
                host=endpoint.host,
                raw_query=message.raw_query,
            )
        case "Request received":
            kind = "receive_request"
            normalized_message = RequestMessage(
                method=message.method,
                path=_normalize_path(message.path),
                raw_query=message.raw_query,
            )
        case "Request routed":
            kind = "request_routed"
            normalized_message = RoutedRequestMessage(
                method=message.method,
                path=_normalize_path(message.path),
                pattern=message.pattern,
            )
        case "Response sent":
            kind = "send_response"
            normalized_message = ResponseMessage(
                method=message.method,
                path=_normalize_path(message.path),
                status_code=_parse_status_code(message.code),
            )
        case "Response received":
            kind = "receive_response"
            normalized_message = ResponseMessage(
                method=message.method,
                path=_normalize_path(message.path),
                status_code=_parse_status_code(message.status_code),
            )
    payload = raw.model_dump(exclude={"kind", "message", "context", "request_id"})
    payload.update(
        schema_version=1,
        kind=kind,
        context=(raw.context or RawContext()).model_dump(),
        message=normalized_message,
    )
    return EVENT_ADAPTER.validate_python(payload)
